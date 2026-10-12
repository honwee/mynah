package rtc

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// A peer built after ConfigureICE must offer a passive TCP host candidate at
// the advertised NAT IP — that is the whole SSH-tunnel story.
func TestConfigureICETCPCandidate(t *testing.T) {
	if err := ConfigureICE("127.0.0.1:0", []string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apiMu.Lock(); api = nil; apiMu.Unlock() })
	p, err := New(nil, "h264")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	offer, err := p.PC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(p.PC)
	if err := p.PC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	sdp := p.PC.LocalDescription().SDP
	var tcp bool
	for _, l := range strings.Split(sdp, "\n") {
		if strings.HasPrefix(l, "a=candidate:") && strings.Contains(l, " tcp ") && strings.Contains(l, "tcptype passive") && strings.Contains(l, " 127.0.0.1 ") {
			tcp = true
		}
	}
	if !tcp {
		t.Fatalf("no passive tcp host candidate at 127.0.0.1 in:\n%s", sdp)
	}
}
