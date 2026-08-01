package protocol

import (
	"net"
	"strings"
	"testing"
)

// hsResult captures one side of a handshake.
type hsResult struct {
	peer string
	err  error
}

// handshake runs both halves concurrently over an in-memory pipe with the given
// versions and the client's v2 preference, returning both sides' results.
func handshake(serverToken, clientToken, serverVer, clientVer string, clientV2 bool) (server, client hsResult) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	done := make(chan hsResult, 1)
	go func() {
		peer, err := ClientHandshake(a, clientToken, clientVer, clientV2)
		done <- hsResult{peer, err}
	}()
	peer, err := ServerHandshake(b, serverToken, serverVer)
	server = hsResult{peer, err}
	client = <-done
	return
}

func TestHandshakeSucceedsWithMatchingToken(t *testing.T) {
	s, c := handshake("shared-token", "shared-token", "v1.0.0", "v1.0.0", true)
	if s.err != nil {
		t.Errorf("server side: %v", s.err)
	}
	if c.err != nil {
		t.Errorf("client side: %v", c.err)
	}
}

// TestVersionExchange is the property the cross-version warning rests on: two v2
// peers each learn the other's version.
func TestVersionExchange(t *testing.T) {
	s, c := handshake("tok", "tok", "v0.7.0", "v0.6.1", true)
	if s.err != nil || c.err != nil {
		t.Fatalf("handshake failed: server=%v client=%v", s.err, c.err)
	}
	if s.peer != "v0.6.1" {
		t.Errorf("server saw client version %q, want v0.6.1", s.peer)
	}
	if c.peer != "v0.7.0" {
		t.Errorf("client saw server version %q, want v0.7.0", c.peer)
	}
}

// TestV1ClientAgainstV2Server covers an old client (no version exchange) linking
// to a new server: it must still authenticate, and the server reports the peer
// as unknown rather than failing.
func TestV1ClientAgainstV2Server(t *testing.T) {
	s, c := handshake("tok", "tok", "v0.7.0", "v0.6.0", false) // client uses v1
	if s.err != nil || c.err != nil {
		t.Fatalf("v1 client / v2 server handshake failed: server=%v client=%v", s.err, c.err)
	}
	if s.peer != UnknownVersion {
		t.Errorf("server saw %q for a v1 client, want %q", s.peer, UnknownVersion)
	}
	if c.peer != UnknownVersion {
		t.Errorf("v1 client saw %q, want %q", c.peer, UnknownVersion)
	}
}

// TestV2ClientAgainstV1Server covers a new client reaching an old, v1-only
// server: the client must get ErrTryV1 so it can fall back, keeping the tunnel
// alive across the version gap.
func TestV2ClientAgainstV1Server(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// The server is a "v1-only" build: it only accepts the v1 magic. Simulate it
	// by reading the magic and closing on anything that is not BKF1.
	go func() {
		var m [4]byte
		if _, err := readFull(b, m[:]); err != nil {
			return
		}
		if m != magicV1 {
			b.Close() // v1-only server rejects the v2 magic
		}
	}()

	_, err := ClientHandshake(a, "tok", "v0.7.0", true)
	if err != ErrTryV1 {
		t.Fatalf("v2 client against a v1-only server: got %v, want ErrTryV1", err)
	}
}

func TestHandshakeRejectsWrongToken(t *testing.T) {
	s, c := handshake("right-token", "wrong-token", "v1", "v1", true)
	if s.err == nil {
		t.Error("server accepted a client with the wrong token")
	}
	if c.err == nil {
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

	_, err := ServerHandshake(b, "token", "v1")
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

// readFull is io.ReadFull, imported locally to keep the test's import list short.
func readFull(r net.Conn, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := r.Read(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
