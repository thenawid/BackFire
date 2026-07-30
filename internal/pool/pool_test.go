package pool

import (
	"context"
	"net"
	"testing"
	"time"
)

// pipePair returns two ends of an in-memory connection.
func pipePair() (net.Conn, net.Conn) { return net.Pipe() }

func TestRoleRoundTrip(t *testing.T) {
	for _, want := range []Role{RoleData, RoleControl} {
		a, b := pipePair()
		go func() {
			if err := WriteRole(a, want); err != nil {
				t.Errorf("WriteRole: %v", err)
			}
		}()
		got, err := ReadRole(b)
		if err != nil {
			t.Fatalf("ReadRole: %v", err)
		}
		if got != want {
			t.Errorf("role round-trip: got %d, want %d", got, want)
		}
		a.Close()
		b.Close()
	}
}

func TestReadRoleRejectsUnknown(t *testing.T) {
	a, b := pipePair()
	go func() { a.Write([]byte{99}); a.Close() }()
	if _, err := ReadRole(b); err == nil {
		t.Fatal("expected an error for an unknown role byte")
	}
	b.Close()
}

func TestReadyPutGet(t *testing.T) {
	r := NewReady(time.Minute)
	defer r.Close()

	c1, _ := pipePair()
	if !r.Put(c1) {
		t.Fatal("Put on an open queue returned false")
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
	got, err := r.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != c1 {
		t.Error("Get returned a different connection than was parked")
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d after Get, want 0", r.Len())
	}
}

// TestReadyGetWaitsForPut covers the case the pool exists for: a connection
// arrives before the client has refilled, and Get must block rather than fail.
func TestReadyGetWaitsForPut(t *testing.T) {
	r := NewReady(time.Minute)
	defer r.Close()

	c1, _ := pipePair()
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.Put(c1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := r.Get(ctx)
	if err != nil {
		t.Fatalf("Get should have waited for the Put: %v", err)
	}
	if got != c1 {
		t.Error("Get returned the wrong connection")
	}
}

func TestReadyGetHonoursContext(t *testing.T) {
	r := NewReady(time.Minute)
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Get(ctx); err == nil {
		t.Fatal("expected Get to fail once the context expired")
	}
}

// TestReadySkipsStale asserts a link parked longer than the idle timeout is
// discarded rather than handed to a caller, since a stale NAT mapping would make
// it silently dead.
func TestReadySkipsStale(t *testing.T) {
	r := NewReady(10 * time.Millisecond)
	defer r.Close()

	stale, _ := pipePair()
	r.Put(stale)
	time.Sleep(40 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.Get(ctx); err == nil {
		t.Fatal("expected no usable link — the only parked one was stale")
	}
}

func TestReadyReapStale(t *testing.T) {
	r := NewReady(10 * time.Millisecond)
	defer r.Close()

	for i := 0; i < 3; i++ {
		c, _ := pipePair()
		r.Put(c)
	}
	time.Sleep(40 * time.Millisecond)
	if n := r.ReapStale(); n != 3 {
		t.Errorf("ReapStale reaped %d, want 3", n)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d after reaping, want 0", r.Len())
	}
}

func TestReadyClosedRejectsPut(t *testing.T) {
	r := NewReady(time.Minute)
	r.Close()
	c, _ := pipePair()
	if r.Put(c) {
		t.Error("Put on a closed queue should return false so the caller can hang up")
	}
}
