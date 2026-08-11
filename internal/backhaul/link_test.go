package backhaul

import (
	"errors"
	"testing"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/utils"
)

// fakeCarrier feeds pre-canned frames to Receive and records what is Sent, so
// the link logic can be exercised without a socket.
type fakeCarrier struct {
	in   chan []byte
	sent chan []byte
}

func newFakeCarrier() *fakeCarrier {
	return &fakeCarrier{in: make(chan []byte, 16), sent: make(chan []byte, 16)}
}

func (c *fakeCarrier) Send(frame []byte) error {
	b := make([]byte, len(frame))
	copy(b, frame)
	c.sent <- b
	return nil
}

func (c *fakeCarrier) Receive() ([]byte, error) {
	frame, ok := <-c.in
	if !ok {
		return nil, errors.New("carrier closed")
	}
	return frame, nil
}

func (c *fakeCarrier) Close() error { close(c.in); return nil }

// fakeTun records the packets written to it and never produces reads.
type fakeTun struct{ written chan []byte }

func newFakeTun() *fakeTun { return &fakeTun{written: make(chan []byte, 16)} }

func (t *fakeTun) Read(p []byte) (int, error) { select {} } // never returns
func (t *fakeTun) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	t.written <- b
	return len(p), nil
}

func newTestEngine(t *testing.T) (*Engine, *metrics.Tunnel) {
	t.Helper()
	e, err := New(config.BackhaulConfig{Token: "shared-secret", MTU: 1400}, config.RoleServer, utils.NewLogger("error"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats := metrics.NewRegistry().Register("t", "server", "backhaul", "udp", 0, nil)
	return e.WithMetrics(stats), stats
}

// TestLinkComesUpOnPeerFrame is the property the honest status rests on: the
// link is down until an authenticated frame arrives, and a keepalive is enough
// to bring it up — and is never written to the interface as if it were a packet.
func TestLinkComesUpOnPeerFrame(t *testing.T) {
	e, stats := newTestEngine(t)
	if stats.Snapshot().Linked {
		t.Fatal("link should start down")
	}

	car := newFakeCarrier()
	tun := newFakeTun()
	go e.carrierToTun(car, tun)

	// Deliver a sealed keepalive, as the peer would.
	frame, err := e.framer.Seal(keepalivePayload)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	car.in <- frame

	// The link must come up…
	waitLinked(t, stats, true)
	// …the keepalive reply must be sent back on first contact…
	select {
	case <-car.sent:
	case <-time.After(time.Second):
		t.Fatal("no keepalive reply was sent on first contact")
	}
	// …and no keepalive is ever injected into the interface.
	select {
	case p := <-tun.written:
		t.Fatalf("keepalive was written to the interface as a packet: %v", p)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRealPacketReachesTun checks an ordinary authenticated packet is delivered
// to the interface (and also lifts the link).
func TestRealPacketReachesTun(t *testing.T) {
	e, stats := newTestEngine(t)
	car := newFakeCarrier()
	tun := newFakeTun()
	go e.carrierToTun(car, tun)

	packet := []byte{0x45, 0x00, 0x00, 0x14} // looks like the start of an IPv4 header
	frame, _ := e.framer.Seal(packet)
	car.in <- frame

	select {
	case got := <-tun.written:
		if string(got) != string(packet) {
			t.Fatalf("tun got %v, want %v", got, packet)
		}
	case <-time.After(time.Second):
		t.Fatal("packet never reached the interface")
	}
	waitLinked(t, stats, true)
}

// TestLinkStale pins the watchdog rule: a fresh receipt is live, a stale one is
// not, and a link that never received anything is down.
func TestLinkStale(t *testing.T) {
	e, _ := newTestEngine(t)
	now := time.Now()

	if !e.linkStale(now) {
		t.Error("a link that never heard from the peer should be stale")
	}
	e.lastRecv.Store(now.UnixNano())
	if e.linkStale(now.Add(keepaliveInterval)) {
		t.Error("a recently-heard link should not be stale")
	}
	if !e.linkStale(now.Add(linkTimeout + time.Second)) {
		t.Error("a link silent past the timeout should be stale")
	}
}

func waitLinked(t *testing.T, stats *metrics.Tunnel, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stats.Snapshot().Linked == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("link never became %v", want)
}
