// Package backhaul implements the layer-3 tunnel family: a TUN device whose IP
// packets are encrypted and carried, in camouflage, inside another IP protocol
// to the peer.
//
// This file is the wire format, kept pure and dependency-free so it can be
// tested exhaustively without a TUN device or a raw socket. Each TUN packet
// becomes one carrier frame:
//
//	nonce (12 bytes) || AES-256-GCM( packet )
//
// The key is derived from the shared token, so both ends encrypt and decrypt
// without exchanging anything, and every frame is individually authenticated —
// which is what lets a connectionless carrier like ICMP or a raw IP protocol
// carry the tunnel safely, with no handshake to fingerprint.
package backhaul

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	nonceLen = 12
	// keyInfo binds the derived key to this protocol and version, so the same
	// token used elsewhere (a BackPack tunnel, say) never yields the same key.
	keyInfo = "backfire-backhaul-frame-v1"
)

// Framer encrypts and decrypts carrier frames under a token-derived key.
type Framer struct {
	aead cipher.AEAD
}

// NewFramer derives the frame key from the token and builds the AEAD.
func NewFramer(token string) (*Framer, error) {
	key := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(token), nil, []byte(keyInfo))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Framer{aead: aead}, nil
}

// Overhead is how many bytes a frame adds to a packet, which the caller needs
// to size the TUN MTU so an encrypted frame still fits the carrier.
func (f *Framer) Overhead() int { return nonceLen + f.aead.Overhead() }

// Seal turns one TUN packet into a carrier frame with a fresh random nonce.
func (f *Framer) Seal(packet []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Prefix the nonce, then append the ciphertext after it in one buffer.
	out := make([]byte, nonceLen, nonceLen+len(packet)+f.aead.Overhead())
	copy(out, nonce)
	return f.aead.Seal(out, nonce, packet, nil), nil
}

// Open recovers the TUN packet from a carrier frame, rejecting anything that
// fails authentication — which is how frames from a wrong-token peer, or random
// noise that happened to arrive on a raw socket, are discarded.
func (f *Framer) Open(frame []byte) ([]byte, error) {
	if len(frame) < nonceLen+f.aead.Overhead() {
		return nil, fmt.Errorf("frame too short: %d bytes", len(frame))
	}
	nonce := frame[:nonceLen]
	return f.aead.Open(nil, nonce, frame[nonceLen:], nil)
}
