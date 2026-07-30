package metrics

import (
	"testing"
	"time"
)

func TestCountersAccumulate(t *testing.T) {
	r := NewRegistry()
	tu := r.Register("t1", "server", "tcpmux", 6060, []int{443})

	tu.AddRx(1000)
	tu.AddRx(500)
	tu.AddTx(250)
	// Negative or zero counts are noise from a failed read and must not corrupt
	// the totals.
	tu.AddRx(-99)

	s := tu.Snapshot()
	if s.RxBytes != 1500 || s.TxBytes != 250 {
		t.Errorf("counters = rx %d / tx %d, want 1500 / 250", s.RxBytes, s.TxBytes)
	}
	if s.Total != 1750 {
		t.Errorf("total = %d, want 1750", s.Total)
	}
}

func TestConnectionCounts(t *testing.T) {
	r := NewRegistry()
	tu := r.Register("t1", "server", "tcp", 6060, nil)

	tu.OpenConn()
	tu.OpenConn()
	tu.CloseConn()

	s := tu.Snapshot()
	if s.Conns != 1 {
		t.Errorf("open connections = %d, want 1", s.Conns)
	}
	if s.TotalConn != 2 {
		t.Errorf("total connections = %d, want 2 (a close must not reduce the lifetime count)", s.TotalConn)
	}
}

// TestSampleComputesRates is the property the sparkline depends on: a rate is
// the byte delta over the elapsed time between two samples.
func TestSampleComputesRates(t *testing.T) {
	r := NewRegistry()
	tu := r.Register("t1", "server", "tcp", 6060, nil)

	base := time.Now()
	tu.sample(base) // first sample establishes the baseline, no rate yet

	tu.AddRx(10000)
	tu.sample(base.Add(10 * time.Second))

	s := tu.Snapshot()
	if len(s.History) != 2 {
		t.Fatalf("history has %d points, want 2", len(s.History))
	}
	if got := s.History[1].RxRate; got != 1000 {
		t.Errorf("rx rate = %.1f B/s, want 1000 (10000 bytes over 10s)", got)
	}
	if s.RxRate != 1000 {
		t.Errorf("snapshot rx rate = %.1f, want the newest sample's 1000", s.RxRate)
	}
}

// TestHistoryIsBounded guards the ring buffer: a long-lived tunnel must not
// accumulate samples without limit.
func TestHistoryIsBounded(t *testing.T) {
	r := NewRegistry()
	tu := r.Register("t1", "server", "tcp", 6060, nil)

	base := time.Now()
	for i := 0; i < historyLen*2; i++ {
		tu.sample(base.Add(time.Duration(i) * time.Second))
	}
	if got := len(tu.Snapshot().History); got != historyLen {
		t.Errorf("history length = %d, want it capped at %d", got, historyLen)
	}
}

func TestPingAndLinked(t *testing.T) {
	r := NewRegistry()
	tu := r.Register("t1", "client", "kcp", 6060, nil)

	if s := tu.Snapshot(); s.PingMs != -1 {
		t.Errorf("unmeasured ping = %.1f, want -1 to mean unknown", s.PingMs)
	}
	tu.SetPing(147 * time.Millisecond)
	tu.SetLinked(true)

	s := tu.Snapshot()
	if s.PingMs < 146 || s.PingMs > 148 {
		t.Errorf("ping = %.1f ms, want ~147", s.PingMs)
	}
	if !s.Linked {
		t.Error("linked was not recorded")
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	r := NewRegistry()
	a := r.Register("t1", "server", "tcp", 6060, nil)
	b := r.Register("t1", "server", "tcp", 6060, nil)
	if a != b {
		t.Error("registering the same name twice created a second tunnel")
	}
	if got := len(r.Snapshots()); got != 1 {
		t.Errorf("registry holds %d tunnels, want 1", got)
	}
}

func TestTotals(t *testing.T) {
	states := []State{
		{Snapshot: Snapshot{RxBytes: 100, TxBytes: 10, RxRate: 5, TxRate: 1, Linked: true}},
		{Snapshot: Snapshot{RxBytes: 200, TxBytes: 20, RxRate: 7, TxRate: 2, Linked: false}},
	}
	rx, tx, rxRate, txRate, linked := Totals(states)
	if rx != 300 || tx != 30 {
		t.Errorf("bytes = %d / %d, want 300 / 30", rx, tx)
	}
	if rxRate != 12 || txRate != 3 {
		t.Errorf("rates = %.0f / %.0f, want 12 / 3", rxRate, txRate)
	}
	if linked != 1 {
		t.Errorf("linked = %d, want 1", linked)
	}
}
