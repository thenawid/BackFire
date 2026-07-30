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

// magic prefixes every handshake so a peer speaking a different protocol (or a
// stray port scanner) is rejected immediately instead of hanging.
var magic = [4]byte{'B', 'K', 'F', '1'}

const (
	challengeLen = 32
	proofLen     = 32
	// handshakeTimeout bounds the whole exchange so a silent peer cannot pin a
	// goroutine open forever.
	handshakeTimeout = 15 * time.Second

	statusOK   byte = 0
	statusFail byte = 1
)

// proof computes HMAC-SHA256(token, magic||challenge).
func proof(token string, challenge []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(magic[:])
	mac.Write(challenge)
	return mac.Sum(nil)
}

// ServerHandshake runs the exposed side of the exchange over an accepted
// connection: it sends the magic and a fresh random challenge, reads the
// client's proof and verifies it in constant time.
func ServerHandshake(conn net.Conn, token string) error {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	challenge := make([]byte, challengeLen)
	if _, err := rand.Read(challenge); err != nil {
		return fmt.Errorf("generate challenge: %w", err)
	}
	if _, err := conn.Write(magic[:]); err != nil {
		return fmt.Errorf("write magic: %w", err)
	}
	if _, err := conn.Write(challenge); err != nil {
		return fmt.Errorf("write challenge: %w", err)
	}

	got := make([]byte, proofLen)
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("read proof: %w", err)
	}
	want := proof(token, challenge)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		_, _ = conn.Write([]byte{statusFail})
		return fmt.Errorf("client failed token authentication")
	}
	if _, err := conn.Write([]byte{statusOK}); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

// ClientHandshake runs the origin side over a freshly dialed connection: it
// checks the magic, answers the challenge with its proof and reads the result.
func ClientHandshake(conn net.Conn, token string) error {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	var gotMagic [4]byte
	if _, err := io.ReadFull(conn, gotMagic[:]); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if gotMagic != magic {
		return fmt.Errorf("bad magic %q — not a backfire server", gotMagic)
	}
	challenge := make([]byte, challengeLen)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	if _, err := conn.Write(proof(token, challenge)); err != nil {
		return fmt.Errorf("write proof: %w", err)
	}
	status := make([]byte, 1)
	if _, err := io.ReadFull(conn, status); err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if status[0] != statusOK {
		return fmt.Errorf("server rejected token")
	}
	return nil
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
