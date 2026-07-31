package backhaul

import (
	"fmt"
	"net"
	"sync"

	"github.com/thenawid/backfire/config"
	"golang.org/x/sys/unix"
)

// rawCarrier carries frames inside a raw IP protocol — ICMP echo, or the bare
// payload of protocols 4/47/112/253 — so the traffic wears the shape of ping,
// IP-in-IP, GRE, VRRP or an experimental protocol. It is the only carrier that
// can forge its source address, which is what the spoof option turns on.
//
// It needs CAP_NET_RAW, so it only works as root or with that capability
// granted; the engine reports a clear error otherwise rather than failing
// obscurely.
type rawCarrier struct {
	fd       int
	proto    int
	icmp     bool
	spoof    bool
	spoofSrc net.IP
	dst      net.IP // peer public address; learned on the server

	mu   sync.Mutex
	seq  uint16
	peer net.IP
}

func newRawCarrier(p carrierParams) (carrier, error) {
	proto := p.cfg.Carrier.Protocol()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, proto)
	if err != nil {
		return nil, fmt.Errorf("raw %s socket (needs CAP_NET_RAW / root): %w", p.cfg.Carrier, err)
	}

	c := &rawCarrier{
		fd:    fd,
		proto: proto,
		icmp:  p.cfg.Carrier == config.CarrierICMP,
		spoof: p.cfg.Spoof,
		peer:  p.peer,
		dst:   p.peer,
	}
	if c.spoof {
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_HDRINCL, 1); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("enable IP_HDRINCL for spoofing: %w", err)
		}
		src := net.ParseIP(p.cfg.SpoofSource)
		if src == nil {
			// No explicit forged source: use a random address so each restart
			// looks like a different origin.
			src = randomIPv4()
		}
		c.spoofSrc = src
	}
	return c, nil
}

func (c *rawCarrier) Send(frame []byte) error {
	c.mu.Lock()
	dst := c.dst
	c.mu.Unlock()
	if dst == nil {
		return nil // server has not yet learned the peer
	}

	payload := frame
	if c.icmp {
		payload = c.wrapICMP(frame)
	}

	packet := payload
	if c.spoof {
		hdr, err := buildIPv4(c.spoofSrc, dst, c.proto, len(payload))
		if err != nil {
			return err
		}
		packet = append(hdr, payload...)
	}

	var addr [4]byte
	copy(addr[:], dst.To4())
	return unix.Sendto(c.fd, packet, 0, &unix.SockaddrInet4{Addr: addr})
}

func (c *rawCarrier) Receive() ([]byte, error) {
	buf := make([]byte, 65535)
	for {
		n, from, err := unix.Recvfrom(c.fd, buf, 0)
		if err != nil {
			return nil, err
		}
		payload, ok := c.stripHeaders(buf[:n])
		if !ok {
			continue // not a frame we produced; skip it
		}
		// Learn / confirm the peer address for a server that dials nobody.
		if sa, ok := from.(*unix.SockaddrInet4); ok {
			src := net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
			c.mu.Lock()
			if c.dst == nil {
				c.dst = src
			}
			c.peer = src
			c.mu.Unlock()
		}
		return payload, nil
	}
}

// stripHeaders removes the IP header (always present on a received raw packet)
// and, for ICMP, the echo header, returning the frame payload. The bool is false
// when the packet is not shaped like one of ours.
func (c *rawCarrier) stripHeaders(pkt []byte) ([]byte, bool) {
	if len(pkt) < ipv4HeaderLen {
		return nil, false
	}
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < ipv4HeaderLen || len(pkt) < ihl {
		return nil, false
	}
	body := pkt[ihl:]
	if !c.icmp {
		return body, true
	}
	// ICMP: expect an echo reply (type 0) carrying our frame as its data.
	if len(body) < icmpHeaderLen || body[0] != icmpEchoReply {
		return nil, false
	}
	return body[icmpHeaderLen:], true
}

func (c *rawCarrier) Close() error { return unix.Close(c.fd) }

const (
	icmpHeaderLen = 8
	icmpEchoReply = 0
)

// wrapICMP frames a payload as an ICMP echo reply. Reply (type 0), not request,
// so the kernel does not auto-respond to our own packets.
func (c *rawCarrier) wrapICMP(frame []byte) []byte {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()

	msg := make([]byte, icmpHeaderLen+len(frame))
	msg[0] = icmpEchoReply
	msg[1] = 0
	// id = 0xB1F5 ("backfire"-ish), seq increments.
	msg[4] = 0xB1
	msg[5] = 0xF5
	msg[6] = byte(seq >> 8)
	msg[7] = byte(seq)
	copy(msg[icmpHeaderLen:], frame)

	cs := checksum(msg)
	msg[2] = byte(cs >> 8)
	msg[3] = byte(cs)
	return msg
}
