package protocol

import (
	"net"
	"strings"
	"testing"
)

// handshake runs both halves concurrently over an in-memory pipe and returns the
// error each side saw.
func handshake(serverToken, clientToken string) (serverErr, clientErr error) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	done := make(chan error, 1)
	go func() { done <- ClientHandshake(a, clientToken) }()
	serverErr = ServerHandshake(b, serverToken)
	clientErr = <-done
	return
}

func TestHandshakeSucceedsWithMatchingToken(t *testing.T) {
	sErr, cErr := handshake("shared-token", "shared-token")
	if sErr != nil {
		t.Errorf("server side: %v", sErr)
	}
	if cErr != nil {
		t.Errorf("client side: %v", cErr)
	}
}

func TestHandshakeRejectsWrongToken(t *testing.T) {
	sErr, cErr := handshake("right-token", "wrong-token")
	if sErr == nil {
		t.Error("server accepted a client with the wrong token")
	}
	if cErr == nil {
		t.Error("client was not told its token was rejected")
	}
}

// TestServerStaysSilentOnBadMagic is the stealth property: something that
// connects without speaking the protocol must learn nothing. The server must
// write no bytes at all before it has seen valid magic.
func TestServerStaysSilentOnBadMagic(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go func() {
		// A scanner sending four bytes of noise, then listening.
		a.Write([]byte("HTTP"))
	}()

	err := ServerHandshake(b, "token")
	if err == nil {
		t.Fatal("server accepted a peer that never sent the magic")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Errorf("expected a magic-related error, got: %v", err)
	}
}

func TestTargetRoundTrip(t *testing.T) {
	for _, want := range []string{"127.0.0.1:443", "example.internal:8080", "[::1]:22"} {
		a, b := net.Pipe()
		go func() { WriteTarget(a, want) }()
		got, err := ReadTarget(b)
		if err != nil {
			t.Fatalf("ReadTarget(%q): %v", want, err)
		}
		if got != want {
			t.Errorf("target round-trip: got %q, want %q", got, want)
		}
		a.Close()
		b.Close()
	}
}

func TestWriteTargetRejectsOutOfRange(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if err := WriteTarget(a, ""); err == nil {
		t.Error("expected an error for an empty target")
	}
	if err := WriteTarget(a, strings.Repeat("x", maxTargetLen+1)); err == nil {
		t.Error("expected an error for an over-long target")
	}
}

// TestReadTargetRejectsHostileLength guards the allocation in ReadTarget against
// a peer claiming a huge length.
func TestReadTargetRejectsHostileLength(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go func() { a.Write([]byte{0xFF, 0xFF}) }() // 65535, far over the cap
	if _, err := ReadTarget(b); err == nil {
		t.Fatal("expected an error for an out-of-range target length")
	}
}
