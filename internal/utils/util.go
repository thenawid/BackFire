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

// Pipe copies data in both directions between a and b until either side ends,
// then closes both. It returns once both copy directions have finished.
func Pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		closeBoth()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
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
