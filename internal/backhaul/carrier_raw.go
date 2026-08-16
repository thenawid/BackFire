package backhaul

import (
	"fmt"
	"net"
	"os"
	"strings"
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
	isServer bool
	spoof    bool
	spoofSrc net.IP
	dst      net.IP // peer public address; learned on the server

	// restoreEchoIgnore restores net.ipv4.icmp_echo_ignore_all to its previous
	// value on Close; nil when it was not changed.
	restoreEchoIgnore func()

	// icmpID is the ICMP echo identifier that stands in for a port on the ICMP
	// carrier: both ends must use the same value, and it lets several ICMP
	// tunnels share the host, each demultiplexed by its id.
	icmpID uint16

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
		fd:       fd,
		proto:    proto,
		icmp:     p.cfg.Carrier == config.CarrierICMP,
		isServer: p.isServer,
		spoof:    p.cfg.Spoof,
		peer:     p.peer,
		dst:      p.peer,
		icmpID:   icmpIDFromPort(p.cfg.Port),
	}
	// An ICMP tunnel rides real ping traffic: the client sends echo requests and
	// the server answers with echo replies, which is what lets the frames cross
	// NAT and stateful firewalls that only pass a reply matching a request. But
	// the kernel would ALSO auto-reply to each request, echoing the client's own
	// frame straight back and looping it — so the server suppresses the kernel's
	// replies. The raw socket still receives every request regardless.
	if c.icmp && c.isServer {
		c.restoreEchoIgnore = suppressKernelEchoReplies()
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
	// ICMP: the peer's messages are the opposite type of ours — a server reads
	// the client's echo requests, a client reads the server's echo replies — and
	// must carry our id, so a stray ping is ignored.
	wantType := byte(icmpEchoReply)
	if c.isServer {
		wantType = icmpEchoRequest
	}
	if len(body) < icmpHeaderLen || body[0] != wantType {
		return nil, false
	}
	if uint16(body[4])<<8|uint16(body[5]) != c.icmpID {
		return nil, false
	}
	return body[icmpHeaderLen:], true
}

func (c *rawCarrier) Close() error {
	if c.restoreEchoIgnore != nil {
		c.restoreEchoIgnore()
	}
	return unix.Close(c.fd)
}

const (
	icmpHeaderLen   = 8
	icmpEchoReply   = 0
	icmpEchoRequest = 8
	// defaultICMPID marks our packets ("backfire"-ish) when no port was set, so a
	// stray ping from elsewhere is not mistaken for a tunnel frame.
	defaultICMPID = 0xB1F5
)

// icmpIDFromPort maps the configured tunnel port onto the ICMP echo identifier,
// so ICMP has a "port" like the other carriers. Zero falls back to the default
// marker, keeping tunnels created before the port was asked working unchanged.
func icmpIDFromPort(port int) uint16 {
	if port <= 0 || port > 0xFFFF {
		return defaultICMPID
	}
	return uint16(port)
}

// wrapICMP frames a payload as an ICMP echo message. The client sends echo
// requests (type 8) and the server answers with echo replies (type 0) — the
// same request→reply shape as a real ping, so the traffic passes NAT and
// stateful firewalls that a bare unsolicited reply would not.
func (c *rawCarrier) wrapICMP(frame []byte) []byte {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()

	msg := make([]byte, icmpHeaderLen+len(frame))
	if c.isServer {
		msg[0] = icmpEchoReply
	} else {
		msg[0] = icmpEchoRequest
	}
	msg[1] = 0
	msg[4] = byte(c.icmpID >> 8)
	msg[5] = byte(c.icmpID)
	msg[6] = byte(seq >> 8)
	msg[7] = byte(seq)
	copy(msg[icmpHeaderLen:], frame)

	cs := checksum(msg)
	msg[2] = byte(cs >> 8)
	msg[3] = byte(cs)
	return msg
}

// suppressKernelEchoReplies sets net.ipv4.icmp_echo_ignore_all=1 so the kernel
// stops auto-answering the echo requests the tunnel carries, and returns a
// function that restores the previous value. On any error it does nothing and
// returns a no-op, since the tunnel still works (just with a harmless duplicate
// reply) when the setting cannot be changed.
func suppressKernelEchoReplies() func() {
	const path = "/proc/sys/net/ipv4/icmp_echo_ignore_all"
	prev, err := os.ReadFile(path)
	if err != nil {
		return func() {}
	}
	if strings.TrimSpace(string(prev)) == "1" {
		return func() {} // already set; leave it as we found it
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		return func() {}
	}
	return func() { _ = os.WriteFile(path, prev, 0o644) }
}
