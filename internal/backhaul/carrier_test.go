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
