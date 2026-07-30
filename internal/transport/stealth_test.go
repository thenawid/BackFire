package transport

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
)

// tcpPair returns the two ends of a real loopback TCP connection.
//
// net.Pipe is deliberately not used here: it is fully synchronous, so a Write
// blocks until the peer Reads. The salt exchange has both sides write first,
// which a real socket absorbs into its send buffer without blocking but an
// unbuffered pipe would deadlock on. Testing over a pipe would be testing
// semantics no transport actually has.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := ln.Accept(); ch <- res{c, err} }()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	t.Cleanup(func() { client.Close(); r.c.Close() })
	return client, r.c
}

// stealthPair wires two stealthConns over a real TCP pair. The salt exchange has
// to run concurrently on both sides because each writes before it reads.
func stealthPair(t *testing.T, token string) (*stealthConn, *stealthConn) {
	t.Helper()
	return stealthPairTokens(t, token, token)
}

// stealthPairTokens is stealthPair with a distinct token per side, for the
// mismatch case.
func stealthPairTokens(t *testing.T, tokenA, tokenB string) (*stealthConn, *stealthConn) {
	t.Helper()
	a, b := tcpPair(t)

	type res struct {
		c   *stealthConn
		err error
	}
	ch := make(chan res, 2)
	go func() { c, err := newStealthConn(a, tokenA); ch <- res{c, err} }()
	go func() { c, err := newStealthConn(b, tokenB); ch <- res{c, err} }()

	r1, r2 := <-ch, <-ch
	if r1.err != nil || r2.err != nil {
		t.Fatalf("salt exchange failed: %v / %v", r1.err, r2.err)
	}
	return r1.c, r2.c
}

func TestStealthRoundTrip(t *testing.T) {
	c1, c2 := stealthPair(t, "a-shared-token")

	msg := []byte("the quick brown fox jumps over the lazy dog")
	go func() { c1.Write(msg) }()

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c2, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, msg)
	}
}

// TestStealthLargePayload crosses the record boundary so the chunking path and
// the leftover-plaintext buffer in Read are both exercised.
func TestStealthLargePayload(t *testing.T) {
	c1, c2 := stealthPair(t, "token")

	msg := make([]byte, maxRecordPlaintext*2+1234)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}
	go func() { c1.Write(msg) }()

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c2, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Error("large payload did not survive the record layer intact")
	}
}

func TestStealthBidirectional(t *testing.T) {
	c1, c2 := stealthPair(t, "token")

	go func() { c1.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c2, buf); err != nil {
		t.Fatalf("c2 read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("c2 got %q", buf)
	}

	go func() { c2.Write([]byte("pong")) }()
	if _, err := io.ReadFull(c1, buf); err != nil {
		t.Fatalf("c1 read: %v", err)
	}
	if string(buf) != "pong" {
		t.Errorf("c1 got %q", buf)
	}
}

// TestStealthWrongTokenFailsAuth is the property that matters: a peer with the
// wrong token derives different keys, so the AEAD tag never verifies and no
// plaintext is ever produced.
func TestStealthWrongTokenFailsAuth(t *testing.T) {
	// The salt exchange itself still succeeds — it is unauthenticated by design.
	// The mismatch only surfaces when a record fails to decrypt.
	c1, c2 := stealthPairTokens(t, "right-token", "WRONG-token")

	go func() { c1.Write([]byte("secret")) }()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(c2, buf); err == nil {
		t.Fatal("a mismatched token must not yield readable plaintext")
	}
}

// TestStealthNoPlaintextOnWire asserts the payload never appears verbatim in the
// bytes handed to the underlying connection — the whole point of the transport.
// It captures the wire bytes by pointing a stealthConn's writer at a buffer.
func TestStealthNoPlaintextOnWire(t *testing.T) {
	c1, _ := stealthPair(t, "token")
	secret := []byte("SUPER-SECRET-MARKER-9f3a")

	// Reuse the negotiated keys, but write into a buffer instead of the pipe so
	// the exact wire encoding can be inspected.
	var wire bytes.Buffer
	capture := &stealthConn{
		Conn:        writerConn{Writer: &wire},
		sendAEAD:    c1.sendAEAD,
		sendLenMask: c1.sendLenMask,
	}
	if _, err := capture.Write(secret); err != nil {
		t.Fatalf("write: %v", err)
	}
	if wire.Len() == 0 {
		t.Fatal("nothing was written to the wire")
	}
	if bytes.Contains(wire.Bytes(), secret) {
		t.Error("plaintext marker found in the bytes written to the wire")
	}
}

// writerConn adapts an io.Writer to net.Conn so a stealthConn can write into a
// buffer. Only Write is exercised.
type writerConn struct {
	net.Conn
	io.Writer
}

func (w writerConn) Write(p []byte) (int, error) { return w.Writer.Write(p) }
