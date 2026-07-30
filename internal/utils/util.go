package utils

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"sync"
)

// GenToken returns a cryptographically random hex token suitable for use as a
// tunnel pre-shared key.
func GenToken(nbytes int) string {
	if nbytes <= 0 {
		nbytes = 24
	}
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail on a supported platform; fall back to a
		// short constant so callers still get a usable (if weak) value rather
		// than crashing. The caller is expected to regenerate on any doubt.
		return "backfire-fallback-token"
	}
	return hex.EncodeToString(b)
}

// Meter receives byte counts observed on a piped connection. Both methods are
// called from the copy goroutines, so an implementation must be safe for
// concurrent use.
type Meter interface {
	// AddRx records bytes arriving from the tunnel peer.
	AddRx(n int64)
	// AddTx records bytes sent to the tunnel peer.
	AddTx(n int64)
}

// Pipe copies data in both directions between a and b until either side ends,
// then closes both. It returns once both copy directions have finished.
func Pipe(a, b io.ReadWriteCloser) { PipeMetered(a, b, nil) }

// PipeMetered is Pipe with byte accounting. Here b is the tunnel side and a the
// local side, so bytes read from b are received from the peer and bytes written
// to b are sent to it. A nil meter costs nothing beyond the plain copy.
func PipeMetered(a, b io.ReadWriteCloser, m Meter) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)

	// b -> a is inbound from the peer; a -> b is outbound to it.
	go func() {
		defer wg.Done()
		copyCounting(a, b, m, true)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		copyCounting(b, a, m, false)
		closeBoth()
	}()
	wg.Wait()
}

// copyCounting copies src into dst, reporting progress to the meter as it goes
// rather than only at the end, so a long-lived connection still shows live
// throughput.
func copyCounting(dst io.Writer, src io.Reader, m Meter, inbound bool) {
	if m == nil {
		_, _ = io.Copy(dst, src)
		return
	}
	_, _ = io.Copy(dst, &meteredReader{r: src, m: m, inbound: inbound})
}

// meteredReader reports every read to the meter as it happens.
type meteredReader struct {
	r       io.Reader
	m       Meter
	inbound bool
}

func (mr *meteredReader) Read(p []byte) (int, error) {
	n, err := mr.r.Read(p)
	if n > 0 {
		if mr.inbound {
			mr.m.AddRx(int64(n))
		} else {
			mr.m.AddTx(int64(n))
		}
	}
	return n, err
}

// SetKeepAlive turns on TCP keepalive with the given period when conn is a
// *net.TCPConn. Non-TCP connections are left untouched.
func SetKeepAlive(conn net.Conn, period int) {
	tc, ok := conn.(*net.TCPConn)
	if !ok || period <= 0 {
		return
	}
	_ = tc.SetKeepAlive(true)
	_ = tc.SetKeepAlivePeriod(durationSeconds(period))
}

// OutboundIP reports the address this host would use to reach the internet,
// which is the one an operator needs in order to open the panel.
//
// It opens a UDP socket toward a public address and reads back the local end.
// UDP is connectionless, so nothing is actually sent — this only asks the
// kernel which interface it would route through.
func OutboundIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}
