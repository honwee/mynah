package rtc

import (
	"fmt"
	"net"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// ICE transport options, set once at startup (ConfigureICE) and used by every
// New(). The zero value means pion's defaults: UDP host/srflx candidates only.
//
// Why this exists: on GPU-rental containers (AutoDL, UCloud 优云智算) the
// instance has no public IP and no UDP path in; the only way to a browser is an
// SSH local port forward, which carries TCP. With --ice-tcp cored also offers
// a passive TCP candidate on a fixed port, and with --ice-nat-ip=127.0.0.1 it
// advertises that candidate at the address the browser actually reaches
// through the tunnel. The browser (Chrome/Edge/Safari all do ICE-TCP) connects
// to 127.0.0.1:<tcp port>, the tunnel delivers it here, media flows over TCP.
// Same two flags cover a NAT with a single forwarded TCP port: --ice-nat-ip
// = the public address, --ice-tcp = the forwarded port.
var (
	apiMu sync.Mutex
	api   *webrtc.API
)

// ConfigureICE installs a TCP ICE mux listening on tcpListen ("" = none) and
// an optional NAT 1:1 host-candidate rewrite to natIPs (nil = none). Must be
// called before the first New(); calling it again replaces the API for
// subsequent peers only.
func ConfigureICE(tcpListen string, natIPs []string) error {
	if tcpListen == "" && len(natIPs) == 0 {
		return nil
	}
	se := webrtc.SettingEngine{}
	nets := []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6}
	if tcpListen != "" {
		ln, err := net.Listen("tcp", tcpListen)
		if err != nil {
			return fmt.Errorf("ice-tcp listen %s: %w", tcpListen, err)
		}
		se.SetICETCPMux(webrtc.NewICETCPMux(nil, ln, 32))
		nets = append(nets, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6)
		// The TCP mux hands an accepted connection to the candidate registered
		// for the connection's LOCAL IP. A tunnelled connection (sshd -> this
		// host) arrives on 127.0.0.1, and pion skips loopback when gathering,
		// so without this the connection is accepted and then dropped for
		// having no matching candidate (observed 2026-10-12: TCP ESTAB on the
		// mux port, ICE stuck in checking). Gathering loopback registers it.
		se.SetIncludeLoopbackCandidate(true)
	}
	se.SetNetworkTypes(nets)
	if len(natIPs) > 0 {
		se.SetNAT1To1IPs(natIPs, webrtc.ICECandidateTypeHost)
	}
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return err
	}
	apiMu.Lock()
	api = webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir))
	apiMu.Unlock()
	return nil
}

func newPeerConnection(cfg webrtc.Configuration) (*webrtc.PeerConnection, error) {
	apiMu.Lock()
	a := api
	apiMu.Unlock()
	if a == nil {
		return webrtc.NewPeerConnection(cfg)
	}
	return a.NewPeerConnection(cfg)
}
