// Package protocol defines the tiny framing that sits directly on top of a
// transport connection: a mutual-auth handshake proving both peers hold the
// shared token, and the per-stream header that tells the client which local
// target to dial.
//
// The handshake is a challenge/response over HMAC-SHA256 rather than sending
// the token in the clear, so a passive observer of a plain (non-TLS) transport
// never sees the secret and cannot replay a captured exchange against a fresh,
// randomly-challenged session.
package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// The magic prefixes every handshake so a peer speaking a different protocol
// (or a stray port scanner) is rejected immediately instead of hanging. There
// are two versions:
//
//   - v1 ("BKF1") is the original handshake.
//   - v2 ("BKF2") is identical up to the status byte, then exchanges each side's
//     software version so a peer can warn when the other end is far behind.
//
// A v2 server accepts a v1 client (older build) and simply skips the version
// exchange; a v2 client that reaches a v1-only server falls back to v1. So a
// tunnel keeps working while the two ends are on different versions — updating
// one side never breaks it.
var (
	magicV1 = [4]byte{'B', 'K', 'F', '1'}
	magicV2 = [4]byte{'B', 'K', 'F', '2'}
)

const (
	challengeLen = 32
	proofLen     = 32
	// handshakeTimeout bounds the whole exchange so a silent peer cannot pin a
	// goroutine open forever.
	handshakeTimeout = 15 * time.Second

	statusOK   byte = 0
	statusFail byte = 1

	// maxVersionLen caps the version string a peer may send.
	maxVersionLen = 64
	// UnknownVersion is reported for a peer that predates the version exchange.
	UnknownVersion = "unknown"
)

// ErrTryV1 signals that the server rejected the v2 handshake (it is an older,
// v1-only build); the caller should redial and use v1.
var ErrTryV1 = fmt.Errorf("peer does not speak handshake v2")

// proof computes HMAC-SHA256(token, magic||challenge). The v1 magic is used in
// the MAC for both versions so a v1 and a v2 peer compute the same proof for a
// given challenge.
func proof(token string, challenge []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(magicV1[:])
	mac.Write(challenge)
	return mac.Sum(nil)
}

// The exchange is deliberately client-first:
//
//	client → server   magic
//	server → client   challenge (32 random bytes)
//	client → server   proof = HMAC-SHA256(token, magic||challenge)
//	server → client   status
//
// Two reasons it must be this way round. Practically, a datagram transport's
// listener cannot even observe a peer until that peer sends something, so a
// server-first greeting would deadlock on udp/kcp. And defensively, the server
// emits nothing at all to something that has not already produced the right
// magic, so a port scanner learns only that a socket accepted — never that a
// tunnel lives here.
//
// ServerHandshake runs the exposed side of the exchange over an accepted
// connection: it checks the magic, issues a fresh random challenge, verifies the
// client's proof in constant time, and — for a v2 client — exchanges versions.
// It returns the peer's version, or UnknownVersion for a v1 client.
func ServerHandshake(conn net.Conn, token, myVersion string) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	var gotMagic [4]byte
	if _, err := io.ReadFull(conn, gotMagic[:]); err != nil {
		return "", fmt.Errorf("read magic: %w", err)
	}
	v2 := gotMagic == magicV2
	if gotMagic != magicV1 && !v2 {
		// Say nothing back — an unrecognised peer gets no signal at all.
		return "", fmt.Errorf("bad magic %q — not a backfire client", gotMagic)
	}

	challenge := make([]byte, challengeLen)
	if _, err := rand.Read(challenge); err != nil {
		return "", fmt.Errorf("generate challenge: %w", err)
	}
	if _, err := conn.Write(challenge); err != nil {
		return "", fmt.Errorf("write challenge: %w", err)
	}

	got := make([]byte, proofLen)
	if _, err := io.ReadFull(conn, got); err != nil {
		return "", fmt.Errorf("read proof: %w", err)
	}
	if subtle.ConstantTimeCompare(got, proof(token, challenge)) != 1 {
		_, _ = conn.Write([]byte{statusFail})
		return "", fmt.Errorf("client failed token authentication")
	}
	if _, err := conn.Write([]byte{statusOK}); err != nil {
		return "", fmt.Errorf("write status: %w", err)
	}

	if !v2 {
		return UnknownVersion, nil // a v1 client sends no version
	}
	// v2: server sends its version, then reads the client's.
	if err := writeString(conn, myVersion); err != nil {
		return "", fmt.Errorf("write version: %w", err)
	}
	peer, err := readString(conn, maxVersionLen)
	if err != nil {
		return "", fmt.Errorf("read peer version: %w", err)
	}
	return peer, nil
}

// ClientHandshake runs the origin side over a freshly dialed connection. With
// v2 true it announces v2 and exchanges versions; if the server turns out to be
// v1-only it returns ErrTryV1 so the caller can redial with v2 false. It returns
// the peer's version (UnknownVersion when v2 is false).
func ClientHandshake(conn net.Conn, token, myVersion string, v2 bool) (string, error) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	magic := magicV1
	if v2 {
		magic = magicV2
	}
	if _, err := conn.Write(magic[:]); err != nil {
		return "", fmt.Errorf("write magic: %w", err)
	}
	challenge := make([]byte, challengeLen)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		// A v1-only server rejects the v2 magic by closing after reading it, so a
		// failure to read the challenge on a v2 attempt means "fall back to v1".
		if v2 {
			return "", ErrTryV1
		}
		return "", fmt.Errorf("read challenge: %w", err)
	}
	if _, err := conn.Write(proof(token, challenge)); err != nil {
		return "", fmt.Errorf("write proof: %w", err)
	}
	status := make([]byte, 1)
	if _, err := io.ReadFull(conn, status); err != nil {
		return "", fmt.Errorf("read status: %w", err)
	}
	if status[0] != statusOK {
		return "", fmt.Errorf("server rejected token")
	}

	if !v2 {
		return UnknownVersion, nil
	}
	// v2: client reads the server's version, then sends its own.
	peer, err := readString(conn, maxVersionLen)
	if err != nil {
		return "", fmt.Errorf("read peer version: %w", err)
	}
	if err := writeString(conn, myVersion); err != nil {
		return "", fmt.Errorf("write version: %w", err)
	}
	return peer, nil
}

// writeString frames a short length-prefixed string.
func writeString(w io.Writer, s string) error {
	if len(s) > maxVersionLen {
		s = s[:maxVersionLen]
	}
	var hdr [1]byte
	hdr[0] = byte(len(s))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

// readString reads a length-prefixed string, bounded by max.
func readString(r io.Reader, max int) (string, error) {
	var hdr [1]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := int(hdr[0])
	if n > max {
		return "", fmt.Errorf("string length %d exceeds %d", n, max)
	}
	if n == 0 {
		return UnknownVersion, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// maxTargetLen guards ReadTarget against a hostile length prefix.
const maxTargetLen = 512

// WriteTarget frames a length-prefixed target address onto a freshly opened
// stream. The server calls this immediately after OpenStream.
func WriteTarget(w io.Writer, target string) error {
	if len(target) == 0 || len(target) > maxTargetLen {
		return fmt.Errorf("target length %d out of range", len(target))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(target)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, target)
	return err
}

// ReadTarget reads the length-prefixed target the server wrote at the head of a
// stream. The client calls this before dialing.
func ReadTarget(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n == 0 || int(n) > maxTargetLen {
		return "", fmt.Errorf("target length %d out of range", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
