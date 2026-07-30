package transport

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/utils"
	"golang.org/x/crypto/hkdf"
)

// The stealth transport is a TCP link wrapped in an encrypted record layer whose
// key is derived from the tunnel token. Unlike TLS it has no handshake to
// fingerprint, no certificate, no version or cipher negotiation, and no fixed
// header — the only plaintext on the wire is a 32-byte random salt each side
// sends first, followed by records that are indistinguishable from random bytes.
// Deep packet inspection has nothing to match on.
//
// Framing per record: a 2-byte big-endian length, then that many bytes of
// AES-256-GCM ciphertext (which includes the 16-byte tag). Both the length
// prefix and the payload are encrypted; the length is protected by its own
// keystream so even record boundaries are not readable.
const (
	saltLen      = 32
	lenPrefixLen = 2
	gcmTagLen    = 16
	// maxRecordPlaintext keeps a record comfortably inside common MTUs after
	// framing and tag overhead.
	maxRecordPlaintext = 16 * 1024
	stealthTimeout     = 15 * time.Second
)

// stealthTransport dials/accepts TCP and wraps the result in the record layer.
type stealthTransport struct{}

func (stealthTransport) Listen(cfg config.ServerConfig) (net.Listener, error) {
	raw, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return nil, err
	}
	return &stealthListener{Listener: raw, token: cfg.Token}, nil
}

func (stealthTransport) Dial(ctx context.Context, cfg config.ClientConfig) (net.Conn, error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", cfg.Server)
	if err != nil {
		return nil, err
	}
	utils.SetKeepAlive(raw, cfg.KeepAlive)
	conn, err := newStealthConn(raw, cfg.Token)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// stealthListener wraps every accepted TCP connection in the record layer.
type stealthListener struct {
	net.Listener
	token string
}

// Accept returns a connection whose salt exchange has not run yet. Doing it
// here would let one silent peer stall the whole accept loop for the handshake
// timeout; instead each connection completes its own exchange on first use, in
// the per-connection goroutine that already owns it.
func (l *stealthListener) Accept() (net.Conn, error) {
	raw, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &lazyStealthConn{Conn: raw, token: l.token}, nil
}

// lazyStealthConn defers the record-layer setup to the first Read or Write.
type lazyStealthConn struct {
	net.Conn
	token string

	once  sync.Once
	inner *stealthConn
	err   error
}

// ready runs the salt exchange exactly once, on whichever of Read/Write happens
// first.
func (c *lazyStealthConn) ready() (*stealthConn, error) {
	c.once.Do(func() {
		c.inner, c.err = newStealthConn(c.Conn, c.token)
	})
	return c.inner, c.err
}

func (c *lazyStealthConn) Read(p []byte) (int, error) {
	inner, err := c.ready()
	if err != nil {
		return 0, err
	}
	return inner.Read(p)
}

func (c *lazyStealthConn) Write(p []byte) (int, error) {
	inner, err := c.ready()
	if err != nil {
		return 0, err
	}
	return inner.Write(p)
}

// stealthConn is an encrypted, record-framed net.Conn.
type stealthConn struct {
	net.Conn
	sendAEAD, recvAEAD cipher.AEAD
	sendSeq, recvSeq   uint64
	sendLenMask        cipher.Stream
	recvLenMask        cipher.Stream
	readBuf            []byte // decrypted plaintext not yet returned to the caller
}

// newStealthConn performs the salt exchange and derives directional keys.
//
// Both sides send a random salt and read the peer's. Keys are derived from
// HKDF(token, salt_client || salt_server) with a per-direction label, so the two
// directions never share a keystream and a replayed session cannot reuse keys.
// Which salt is "client" is decided by who wrote first — the dialer — so both
// ends must agree; we settle it by having each side label its own salt.
func newStealthConn(raw net.Conn, token string) (*stealthConn, error) {
	_ = raw.SetDeadline(time.Now().Add(stealthTimeout))
	defer raw.SetDeadline(time.Time{})

	mySalt := make([]byte, saltLen)
	if _, err := rand.Read(mySalt); err != nil {
		return nil, err
	}
	// Write our salt and read theirs. Writing first on both sides is safe: the
	// salts are independent and TCP buffers a 32-byte write without blocking.
	if _, err := raw.Write(mySalt); err != nil {
		return nil, fmt.Errorf("stealth: write salt: %w", err)
	}
	peerSalt := make([]byte, saltLen)
	if _, err := io.ReadFull(raw, peerSalt); err != nil {
		return nil, fmt.Errorf("stealth: read salt: %w", err)
	}

	// Derive four keys from a salt pair ordered canonically (so both ends
	// compute the same material) plus a direction label tied to *our* salt.
	lo, hi := mySalt, peerSalt
	weAreLo := true
	if string(mySalt) > string(peerSalt) {
		lo, hi = peerSalt, mySalt
		weAreLo = false
	}
	salt := append(append([]byte{}, lo...), hi...)

	sendLabel, recvLabel := "backfire-stealth-lo", "backfire-stealth-hi"
	if !weAreLo {
		sendLabel, recvLabel = recvLabel, sendLabel
	}

	sendAEAD, sendMask, err := deriveDirection(token, salt, sendLabel)
	if err != nil {
		return nil, err
	}
	recvAEAD, recvMask, err := deriveDirection(token, salt, recvLabel)
	if err != nil {
		return nil, err
	}

	return &stealthConn{
		Conn:        raw,
		sendAEAD:    sendAEAD,
		recvAEAD:    recvAEAD,
		sendLenMask: sendMask,
		recvLenMask: recvMask,
	}, nil
}

// deriveDirection builds the AEAD and the length-masking keystream for one
// direction of the link.
func deriveDirection(token string, salt []byte, label string) (cipher.AEAD, cipher.Stream, error) {
	// 32 bytes AEAD key + 16 bytes CTR key + 16 bytes CTR IV.
	out := make([]byte, 32+16+16)
	r := hkdf.New(sha256.New, []byte(token), salt, []byte(label))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(out[:32])
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	maskBlock, err := aes.NewCipher(out[32:48])
	if err != nil {
		return nil, nil, err
	}
	return aead, cipher.NewCTR(maskBlock, out[48:64]), nil
}

// nonce builds a deterministic 12-byte nonce from a record counter. Each
// direction has its own key and its own monotonic counter, so a nonce is never
// reused under one key.
func nonce(seq uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[4:], seq)
	return n[:]
}

func (c *stealthConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxRecordPlaintext {
			chunk = chunk[:maxRecordPlaintext]
		}
		ct := c.sendAEAD.Seal(nil, nonce(c.sendSeq), chunk, nil)
		c.sendSeq++

		var hdr [lenPrefixLen]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(ct)))
		// Mask the length with its own keystream so record boundaries are not
		// readable on the wire.
		c.sendLenMask.XORKeyStream(hdr[:], hdr[:])

		if _, err := c.Conn.Write(hdr[:]); err != nil {
			return written, err
		}
		if _, err := c.Conn.Write(ct); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (c *stealthConn) Read(p []byte) (int, error) {
	// Drain any plaintext left over from the previous record first.
	if len(c.readBuf) > 0 {
		n := copy(p, c.readBuf)
		c.readBuf = c.readBuf[n:]
		return n, nil
	}
	var hdr [lenPrefixLen]byte
	if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
		return 0, err
	}
	c.recvLenMask.XORKeyStream(hdr[:], hdr[:])
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n < gcmTagLen || n > maxRecordPlaintext+gcmTagLen {
		return 0, fmt.Errorf("stealth: record length %d out of range", n)
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(c.Conn, ct); err != nil {
		return 0, err
	}
	pt, err := c.recvAEAD.Open(nil, nonce(c.recvSeq), ct, nil)
	if err != nil {
		return 0, fmt.Errorf("stealth: record authentication failed: %w", err)
	}
	c.recvSeq++

	n = copy(p, pt)
	if n < len(pt) {
		c.readBuf = pt[n:]
	}
	return n, nil
}
