package backhaul

import (
	"bytes"
	"net"
	"os"
	"testing"
	"time"

	"github.com/thenawid/backfire/config"
)

// requireRoot skips a test that needs privileges the sandbox may not grant.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root / CAP_NET_RAW")
	}
}

// carrierRoundTrip drives one frame from a client carrier to a server carrier
// and asserts it arrives intact. It exercises the real socket path, not a mock.
func carrierRoundTrip(t *testing.T, c config.BackhaulConfig) {
	t.Helper()
	loop := net.ParseIP("127.0.0.1")

	srv, err := newCarrier(carrierParams{cfg: c, isServer: true})
	if err != nil {
		t.Fatalf("server carrier: %v", err)
	}
	defer srv.Close()

	cli, err := newCarrier(carrierParams{cfg: c, isServer: false, peer: loop})
	if err != nil {
		t.Fatalf("client carrier: %v", err)
	}
	defer cli.Close()

	frame := []byte("a sealed frame across the carrier")

	// The server receives in the background; the client sends until one lands,
	// since a connectionless carrier can drop the very first packet while the
	// server is still setting up its receive.
	got := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		b, err := srv.Receive()
		if err != nil {
			errc <- err
			return
		}
		got <- append([]byte(nil), b...)
	}()

	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := cli.Send(frame); err != nil {
			t.Fatalf("send: %v", err)
		}
		select {
		case b := <-got:
			if !bytes.Equal(b, frame) {
				t.Errorf("frame mismatch: got %q", b)
			}
			return
		case err := <-errc:
			t.Fatalf("receive: %v", err)
		case <-deadline:
			t.Fatal("frame never arrived across the carrier")
		case <-tick.C:
		}
	}
}

func TestUDPCarrierRoundTrip(t *testing.T) {
	carrierRoundTrip(t, config.BackhaulConfig{Carrier: config.CarrierUDP, Port: freeUDPPort(t), Token: "t"})
}

func TestTCPCarrierRoundTrip(t *testing.T) {
	// The TCP carrier's server blocks in Accept inside newCarrier, so build the
	// two ends concurrently.
	c := config.BackhaulConfig{Carrier: config.CarrierTCP, Port: freeTCPPort(t), Token: "t"}
	loop := net.ParseIP("127.0.0.1")

	type carRes struct {
		c   carrier
		err error
	}
	srvCh := make(chan carRes, 1)
	go func() {
		s, err := newCarrier(carrierParams{cfg: c, isServer: true})
		srvCh <- carRes{s, err}
	}()
	time.Sleep(100 * time.Millisecond)

	cli, err := newCarrier(carrierParams{cfg: c, isServer: false, peer: loop})
	if err != nil {
		t.Fatalf("client carrier: %v", err)
	}
	defer cli.Close()

	sr := <-srvCh
	if sr.err != nil {
		t.Fatalf("server carrier: %v", sr.err)
	}
	defer sr.c.Close()

	frame := []byte("tcp carrier frame")
	if err := cli.Send(frame); err != nil {
		t.Fatalf("send: %v", err)
	}
	got, err := sr.c.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if !bytes.Equal(got, frame) {
		t.Errorf("frame mismatch: got %q", got)
	}
}

// TestICMPCarrierRoundTrip drives the raw ICMP carrier over loopback. Both ends
// share one socket view of 127.0.0.1, so this proves the ICMP framing and the
// raw send/receive path, not the peer-learning logic.
func TestICMPCarrierRoundTrip(t *testing.T) {
	requireRoot(t)
	carrierRoundTrip(t, config.BackhaulConfig{Carrier: config.CarrierICMP, Token: "t"})
}

func TestRawGRECarrierRoundTrip(t *testing.T) {
	requireRoot(t)
	carrierRoundTrip(t, config.BackhaulConfig{Carrier: config.CarrierGRE, Token: "t"})
}

// TestICMPCarrierBidirectional proves both legs of the ICMP tunnel: the client's
// echo request reaches the server, and the server's echo reply reaches the
// client. The reply leg is the one that used to be dropped when both ends sent
// the same ICMP type, so it is asserted explicitly here.
func TestICMPCarrierBidirectional(t *testing.T) {
	requireRoot(t)
	loop := net.ParseIP("127.0.0.1")
	cfg := config.BackhaulConfig{Carrier: config.CarrierICMP, Token: "t"}

	srv, err := newCarrier(carrierParams{cfg: cfg, isServer: true})
	if err != nil {
		t.Fatalf("server carrier: %v", err)
	}
	defer srv.Close()
	cli, err := newCarrier(carrierParams{cfg: cfg, isServer: false, peer: loop})
	if err != nil {
		t.Fatalf("client carrier: %v", err)
	}
	defer cli.Close()

	// Client → server (echo request). Retry until one lands so the server can
	// learn the peer, since a connectionless carrier may drop the first packet.
	up := []byte("client-to-server")
	srvGot := receiveAsync(srv)
	if !sendUntil(t, cli, up, srvGot) {
		t.Fatal("client's request never reached the server")
	}

	// Server → client (echo reply): the server now knows the peer.
	down := []byte("server-to-client")
	cliGot := receiveAsync(cli)
	if !sendUntil(t, srv, down, cliGot) {
		t.Fatal("server's reply never reached the client — the return path is broken")
	}
}

// TestICMPPortIsIdentifier proves the tunnel port really acts as the ICMP echo
// id: two ends that share a port pass frames, and a mismatched port does not —
// so ICMP has a working "port" like the other carriers.
func TestICMPPortIsIdentifier(t *testing.T) {
	requireRoot(t)
	loop := net.ParseIP("127.0.0.1")
	mk := func(server bool, port int) carrier {
		c, err := newCarrier(carrierParams{
			cfg:      config.BackhaulConfig{Carrier: config.CarrierICMP, Token: "t", Port: port},
			isServer: server,
			peer:     loop,
		})
		if err != nil {
			t.Fatalf("carrier: %v", err)
		}
		return c
	}

	// Matching ports (7001 both) → the frame crosses.
	srv, cli := mk(true, 7001), mk(false, 7001)
	defer srv.Close()
	defer cli.Close()
	if !sendUntil(t, cli, []byte("same-id"), receiveAsync(srv)) {
		t.Fatal("frame did not cross with matching ports")
	}

	// Mismatched ports (client 7001, server 9999) → nothing should arrive.
	srv2, cli2 := mk(true, 9999), mk(false, 7001)
	defer srv2.Close()
	defer cli2.Close()
	got := receiveAsync(srv2)
	deadline := time.After(1500 * time.Millisecond)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		_ = cli2.Send([]byte("wrong-id"))
		select {
		case <-got:
			t.Fatal("a frame crossed despite mismatched ports (ids)")
		case <-deadline:
			return // good: nothing arrived
		case <-tick.C:
		}
	}
}

// receiveAsync starts one Receive in the background and returns a channel that
// delivers the frame (or nothing, on error).
func receiveAsync(c carrier) <-chan []byte {
	got := make(chan []byte, 1)
	go func() {
		if b, err := c.Receive(); err == nil {
			got <- append([]byte(nil), b...)
		}
	}()
	return got
}

// sendUntil sends frame on a ticker until want arrives on got, or times out.
func sendUntil(t *testing.T, c carrier, frame []byte, got <-chan []byte) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := c.Send(frame); err != nil {
			t.Fatalf("send: %v", err)
		}
		select {
		case b := <-got:
			if !bytes.Equal(b, frame) {
				t.Fatalf("frame mismatch: got %q want %q", b, frame)
			}
			return true
		case <-deadline:
			return false
		case <-tick.C:
		}
	}
}

// TestSpoofedCarrierSends checks the spoof path builds and transmits without
// error over loopback; the forged source is asserted directly in the header
// test.
func TestSpoofedCarrierSends(t *testing.T) {
	requireRoot(t)
	c := config.BackhaulConfig{
		Carrier: config.CarrierICMP, Token: "t",
		Spoof: true, SpoofSource: "203.0.113.99",
	}
	cli, err := newCarrier(carrierParams{cfg: c, isServer: false, peer: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("spoofed carrier: %v", err)
	}
	defer cli.Close()
	if err := cli.Send([]byte("spoofed frame")); err != nil {
		t.Fatalf("spoofed send: %v", err)
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
