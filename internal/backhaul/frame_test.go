package backhaul

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	f, err := NewFramer("a-shared-token")
	if err != nil {
		t.Fatalf("NewFramer: %v", err)
	}
	packet := []byte("this is a fake IP packet payload")
	frame, err := f.Seal(packet)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(frame, packet) {
		t.Error("the sealed frame contains the plaintext packet")
	}
	got, err := f.Open(frame)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, packet) {
		t.Errorf("round-trip mismatch: got %q", got)
	}
}

// TestFrameNonceIsFresh guards against a fixed nonce: two seals of the same
// packet must differ, or GCM's security is void.
func TestFrameNonceIsFresh(t *testing.T) {
	f, _ := NewFramer("token")
	packet := []byte("identical input")
	a, _ := f.Seal(packet)
	b, _ := f.Seal(packet)
	if bytes.Equal(a, b) {
		t.Error("two seals of the same packet were identical — nonce is not fresh")
	}
}

// TestFrameWrongTokenRejected is the property a raw carrier leans on: frames
// from a peer with a different token must fail to open, so unrelated traffic on
// the socket is discarded rather than injected into the interface.
func TestFrameWrongTokenRejected(t *testing.T) {
	sender, _ := NewFramer("right-token")
	receiver, _ := NewFramer("WRONG-token")

	frame, _ := sender.Seal([]byte("secret packet"))
	if _, err := receiver.Open(frame); err == nil {
		t.Fatal("a frame sealed with a different token must not open")
	}
}

func TestFrameRejectsGarbage(t *testing.T) {
	f, _ := NewFramer("token")
	if _, err := f.Open([]byte("too short")); err == nil {
		t.Error("expected an error opening a short frame")
	}
	junk := make([]byte, 200)
	rand.Read(junk)
	if _, err := f.Open(junk); err == nil {
		t.Error("expected an error opening random bytes")
	}
}

func TestFrameLargePacket(t *testing.T) {
	f, _ := NewFramer("token")
	packet := make([]byte, 1400)
	rand.Read(packet)
	frame, err := f.Seal(packet)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := f.Open(frame)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, packet) {
		t.Error("large packet did not survive the frame layer")
	}
	if f.Overhead() != nonceLen+16 {
		t.Errorf("overhead = %d, want %d", f.Overhead(), nonceLen+16)
	}
}

func TestBuildIPv4Spoof(t *testing.T) {
	src := net.ParseIP("203.0.113.7")
	dst := net.ParseIP("198.51.100.9")
	hdr, err := buildIPv4(src, dst, 47, 100) // proto 47 = GRE
	if err != nil {
		t.Fatalf("buildIPv4: %v", err)
	}
	if len(hdr) != ipv4HeaderLen {
		t.Fatalf("header length = %d, want %d", len(hdr), ipv4HeaderLen)
	}
	if hdr[0] != 0x45 {
		t.Errorf("version/IHL byte = %#x, want 0x45", hdr[0])
	}
	if hdr[9] != 47 {
		t.Errorf("protocol = %d, want 47", hdr[9])
	}
	// The forged source must actually be in the header.
	if !bytes.Equal(hdr[12:16], src.To4()) {
		t.Error("the spoofed source address is not in the header")
	}
	if !bytes.Equal(hdr[16:20], dst.To4()) {
		t.Error("the destination address is wrong")
	}
	// The checksum field must verify: summing the whole header yields 0xFFFF.
	if s := verifyChecksum(hdr); s != 0xFFFF {
		t.Errorf("header checksum does not verify (got %#x)", s)
	}
}

func TestBuildIPv4RejectsNonIPv4(t *testing.T) {
	if _, err := buildIPv4(net.ParseIP("::1"), net.ParseIP("198.51.100.9"), 4, 0); err == nil {
		t.Error("expected an error for an IPv6 source")
	}
}

// verifyChecksum sums a header the way a receiver does; a valid header sums to
// all-ones.
func verifyChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return uint16(sum)
}
