package backhaul

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// tcpCarrier carries frames over a single TCP stream, each prefixed with a
// 2-byte length. The server accepts one connection at a time; the client dials
// and, if the link drops, the engine rebuilds the whole carrier.
type tcpCarrier struct {
	ln   net.Listener // server only
	conn net.Conn

	mu     sync.Mutex
	closed bool
}

func newTCPCarrier(p carrierParams) (carrier, error) {
	if p.isServer {
		ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", p.cfg.Port))
		if err != nil {
			return nil, fmt.Errorf("tcp carrier listen: %w", err)
		}
		c := &tcpCarrier{ln: ln}
		conn, err := ln.Accept()
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("tcp carrier accept: %w", err)
		}
		c.conn = conn
		return c, nil
	}
	if p.peer == nil {
		return nil, fmt.Errorf("tcp carrier client needs a peer address")
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("%s:%d", p.peer, p.cfg.Port), 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("tcp carrier dial: %w", err)
	}
	return &tcpCarrier{conn: conn}, nil
}

func (c *tcpCarrier) Send(frame []byte) error {
	if len(frame) > 0xFFFF {
		return fmt.Errorf("frame too large for tcp carrier: %d", len(frame))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(frame)))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *tcpCarrier) Receive() ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *tcpCarrier) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn != nil {
		c.conn.Close()
	}
	if c.ln != nil {
		c.ln.Close()
	}
	return nil
}
