// Package rtc wraps pion WebRTC: a peer with a video (VP8 or H264) + Opus
// audio track, and offer->answer negotiation (replaces the aiortc
// RTCManager/HumanPlayer).
package rtc

import (
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// Peer holds a PeerConnection and its two outbound sample tracks.
type Peer struct {
	PC    *webrtc.PeerConnection
	Video *webrtc.TrackLocalStaticSample
	Audio *webrtc.TrackLocalStaticSample

	kfMu sync.Mutex
	onKF func()
}

// SetKeyframeHandler installs the callback run when the viewer asks for a
// keyframe (RTCP PLI / FIR on the video sender). Without it a lost packet
// freezes the picture until the next periodic IDR (up to a whole GOP).
func (p *Peer) SetKeyframeHandler(fn func()) {
	p.kfMu.Lock()
	p.onKF = fn
	p.kfMu.Unlock()
}

func (p *Peer) keyframeRequested() {
	p.kfMu.Lock()
	fn := p.onKF
	p.kfMu.Unlock()
	if fn != nil {
		fn()
	}
}

// New builds a PeerConnection with a video track of the given codec ("vp8" or
// "h264") + an Opus audio track added. H264 is constrained-baseline
// packetization-mode=1 — the profile Safari/iOS offers and the worker encodes.
func New(stunURLs []string, videoCodec string) (*Peer, error) {
	cfg := webrtc.Configuration{}
	if len(stunURLs) > 0 {
		cfg.ICEServers = []webrtc.ICEServer{{URLs: stunURLs}}
	}
	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	vcap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}
	if videoCodec == "h264" {
		vcap = webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		}
	}
	video, err := webrtc.NewTrackLocalStaticSample(vcap, "video", "mynah")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	audio, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "mynah")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	vs, err := pc.AddTrack(video)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	as, err := pc.AddTrack(audio)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	p := &Peer{PC: pc, Video: video, Audio: audio}
	p.watchVideoRTCP(vs)
	drainRTCP(as)
	return p, nil
}

// Answer applies the remote offer and returns the local answer SDP (with ICE
// candidates gathered, since we use non-trickle signaling like the aiortc path).
func (p *Peer) Answer(offerSDP string) (string, error) {
	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := p.PC.SetRemoteDescription(offer); err != nil {
		return "", err
	}
	answer, err := p.PC.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(p.PC)
	if err := p.PC.SetLocalDescription(answer); err != nil {
		return "", err
	}
	<-gatherComplete
	return p.PC.LocalDescription().SDP, nil
}

func (p *Peer) Close() { _ = p.PC.Close() }

// watchVideoRTCP reads the video sender's RTCP (which also keeps pion's
// NACK/SR interceptors fed) and turns PLI / FIR into keyframe requests.
func (p *Peer) watchVideoRTCP(s *webrtc.RTPSender) {
	go func() {
		for {
			pkts, _, err := s.ReadRTCP()
			if err != nil {
				return
			}
			for _, pkt := range pkts {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					p.keyframeRequested()
				}
			}
		}
	}()
}

// drainRTCP reads (and discards) RTCP so pion interceptors (NACK/SR) function.
func drainRTCP(s *webrtc.RTPSender) {
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := s.Read(buf); err != nil {
				return
			}
		}
	}()
}
