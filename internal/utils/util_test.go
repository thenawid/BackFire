package utils

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingMeter is a Meter that records what the pipe reported.
type countingMeter struct {
	rx, tx atomic.Int64
}

func (m *countingMeter) AddRx(n int64) { m.rx.Add(n) }
func (m *countingMeter) AddTx(n int64) { m.tx.Add(n) }

// TestPipeMeteredCountsBothDirections is what the panel's traffic figures rest
// on: bytes moving each way must be attributed to the right direction.
func TestPipeMeteredCountsBothDirections(t *testing.T) {
	// local <-> a  is the application side; b <-> peer is the tunnel side.
	local, a := net.Pipe()
	b, peer := net.Pipe()

	m := &countingMeter{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); PipeMetered(a, b, m) }()

	inbound := []byte("hello from the peer")
	outbound := []byte("reply from the app")

	// Peer -> local counts as received.
	go func() { peer.Write(inbound) }()
	got := make([]byte, len(inbound))
	if _, err := io.ReadFull(local, got); err != nil {
		t.Fatalf("local read: %v", err)
	}

	// Local -> peer counts as sent.
	go func() { local.Write(outbound) }()
	got2 := make([]byte, len(outbound))
	if _, err := io.ReadFull(peer, got2); err != nil {
		t.Fatalf("peer read: %v", err)
	}

	// Give the metered readers a moment to record before asserting.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.rx.Load() == int64(len(inbound)) && m.tx.Load() == int64(len(outbound)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := m.rx.Load(); got != int64(len(inbound)) {
		t.Errorf("rx = %d, want %d", got, len(inbound))
	}
	if got := m.tx.Load(); got != int64(len(outbound)) {
		t.Errorf("tx = %d, want %d", got, len(outbound))
	}

	local.Close()
	peer.Close()
	wg.Wait()
}

// TestPipeClosesBothSides covers the cleanup contract: when one end goes away,
// the other must not be left open.
func TestPipeClosesBothSides(t *testing.T) {
	local, a := net.Pipe()
	b, peer := net.Pipe()

	done := make(chan struct{})
	go func() { Pipe(a, b); close(done) }()

	local.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Pipe did not return after one side closed")
	}

	// The far end should now be closed too.
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Error("peer end was left readable after the other side closed")
	}
}

func TestGenTokenIsRandomAndSized(t *testing.T) {
	a, b := GenToken(24), GenToken(24)
	if a == b {
		t.Error("two generated tokens were identical")
	}
	if len(a) != 48 { // 24 bytes hex-encoded
		t.Errorf("token length = %d, want 48 hex characters", len(a))
	}
	if GenToken(0) == "" {
		t.Error("a zero size should fall back to a default, not an empty token")
	}
}
