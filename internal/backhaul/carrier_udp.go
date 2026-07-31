package backhaul

import (
	"fmt"
	"net"
	"sync"
)

// udpCarrier carries frames as UDP datagrams. The client dials the peer; the
// server binds and learns the peer's address from the first datagram it
// receives, so a client behind NAT still works.
type udpCarrier struct {
	conn *net.UDPConn

	mu   sync.Mutex
	peer *net.UDPAddr // nil on a server until the first packet arrives
}

func newUDPCarrier(p carrierParams) (carrier, error) {
	if p.isServer {
		addr := &net.UDPAddr{Port: p.cfg.Port}
		conn, err := net.ListenUDP("udp4", addr)
		if err != nil {
			return nil, fmt.Errorf("udp carrier listen: %w", err)
		}
		return &udpCarrier{conn: conn}, nil
	}
	if p.peer == nil {
		return nil, fmt.Errorf("udp carrier client needs a peer address")
	}
	raddr := &net.UDPAddr{IP: p.peer, Port: p.cfg.Port}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("udp carrier dial: %w", err)
	}
	return &udpCarrier{conn: conn, peer: raddr}, nil
}

func (c *udpCarrier) Send(frame []byte) error {
	c.mu.Lock()
	peer := c.peer
	c.mu.Unlock()
	if peer == nil {
		// A server that has not yet heard from its client has nowhere to send.
		return nil
	}
	if c.isConnected() {
		_, err := c.conn.Write(frame)
		return err
	}
	_, err := c.conn.WriteToUDP(frame, peer)
	return err
}

func (c *udpCarrier) Receive() ([]byte, error) {
	buf := make([]byte, 65535)
	n, addr, err := c.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, err
	}
	if addr != nil {
		c.mu.Lock()
		c.peer = addr
		c.mu.Unlock()
	}
	return buf[:n], nil
}

func (c *udpCarrier) Close() error { return c.conn.Close() }

// isConnected reports whether the socket was created with DialUDP (client), in
// which case Write is used instead of WriteToUDP.
func (c *udpCarrier) isConnected() bool { return c.conn.RemoteAddr() != nil }
