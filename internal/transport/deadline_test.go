package transport

import (
	"io"
	"testing"
	"time"
)

// TestReadDoesNotHangForever is a guard on the test harness itself: a read with
// no writer must fail on the socket deadline, not block until the suite times
// out. A hanging test reports nothing useful about which assertion broke.
func TestReadDoesNotHangForever(t *testing.T) {
	_, c2 := stealthPair(t, "token")
	start := time.Now()
	_, err := io.ReadFull(c2, make([]byte, 8))
	if err == nil {
		t.Fatal("expected the read to fail, nothing was ever written")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("read took %s to fail — the deadline is not bounding it", elapsed)
	}
	t.Logf("read failed after %s with: %v", time.Since(start).Round(time.Second), err)
}
