package backhaul

import (
	"encoding/binary"
	"fmt"
	"net"
)

// When a raw carrier spoofs its source address, the kernel will not fill in the
// IP header for us — we ask it not to, with IP_HDRINCL, and build the header by
// hand so the source field can be forged. This file is that header builder,
// kept pure so the forged-source behaviour can be asserted without a socket.

const ipv4HeaderLen = 20

// buildIPv4 assembles a 20-byte IPv4 header for the given protocol carrying
// payload, with the source and destination as specified. src is the (possibly
// forged) source address. The kernel recomputes the checksum for IP_HDRINCL on
// most systems, but we set a correct one anyway so the packet is valid on those
// that do not.
func buildIPv4(src, dst net.IP, protocol int, payloadLen int) ([]byte, error) {
	s := src.To4()
	d := dst.To4()
	if s == nil {
		return nil, fmt.Errorf("source %v is not an IPv4 address", src)
	}
	if d == nil {
		return nil, fmt.Errorf("destination %v is not an IPv4 address", dst)
	}
	total := ipv4HeaderLen + payloadLen
	if total > 0xFFFF {
		return nil, fmt.Errorf("packet too large: %d bytes", total)
	}

	h := make([]byte, ipv4HeaderLen)
	h[0] = 0x45 // version 4, header length 5 words
	h[1] = 0    // DSCP/ECN
	binary.BigEndian.PutUint16(h[2:], uint16(total))
	binary.BigEndian.PutUint16(h[4:], 0) // identification (kernel may overwrite)
	binary.BigEndian.PutUint16(h[6:], 0) // flags + fragment offset
	h[8] = 64                            // TTL
	h[9] = byte(protocol)
	// h[10:12] checksum, filled below
	copy(h[12:16], s)
	copy(h[16:20], d)
	binary.BigEndian.PutUint16(h[10:], checksum(h))
	return h, nil
}

// checksum computes the standard one's-complement IPv4 header checksum.
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}
