// Package metrics tracks what the panel and the bot report: per-tunnel byte
// counters, a rolling history for the sparklines, connection counts and link
// latency.
//
// Counters are updated on the data path, so they are plain atomics — a tunnel
// moving gigabytes must never contend on a mutex just to be observable.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Point is one sample in a tunnel's history.
type Point struct {
	At time.Time `json:"at"`
	// RxRate / TxRate are bytes per second over the sampling interval.
	RxRate float64 `json:"rx_rate"`
	TxRate float64 `json:"tx_rate"`
}

// historyLen is how many samples a tunnel keeps. At the default 10-second
// sampling interval this is a little under 20 minutes of history, which is what
// the panel's sparkline shows.
const historyLen = 114

// Tunnel is the live state of one tunnel.
type Tunnel struct {
	Name      string
	Role      string
	Transport string
	// Port is the tunnel link's own port.
	Port int
	// Forwarded lists the published ports.
	Forwarded []int

	// Counters, updated on the data path.
	rx    atomic.Uint64
	tx    atomic.Uint64
	conns atomic.Int64 // currently open forwarded connections
	total atomic.Int64 // forwarded connections since start

	// linked is 1 while a peer is connected.
	linked atomic.Bool
	// pingMicros is the last measured round trip, in microseconds; 0 = unknown.
	pingMicros atomic.Int64

	mu       sync.Mutex
	history  []Point
	lastRx   uint64
	lastTx   uint64
	lastAt   time.Time
	startedA time.Time
}

// Snapshot is an immutable view of a tunnel, safe to hand to a JSON encoder or a
// template.
type Snapshot struct {
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	Transport string  `json:"transport"`
	Port      int     `json:"port"`
	Forwarded []int   `json:"forwarded"`
	Linked    bool    `json:"linked"`
	RxBytes   uint64  `json:"rx_bytes"`
	TxBytes   uint64  `json:"tx_bytes"`
	Total     uint64  `json:"total_bytes"`
	RxRate    float64 `json:"rx_rate"`
	TxRate    float64 `json:"tx_rate"`
	Conns     int64   `json:"connections"`
	TotalConn int64   `json:"total_connections"`
	// PingMs is the last measured round trip in milliseconds; -1 = unknown.
	PingMs  float64 `json:"ping_ms"`
	Uptime  int64   `json:"uptime_seconds"`
	History []Point `json:"history"`
}

// AddRx records bytes received from the peer.
func (t *Tunnel) AddRx(n int64) {
	if n > 0 {
		t.rx.Add(uint64(n))
	}
}

// AddTx records bytes sent to the peer.
func (t *Tunnel) AddTx(n int64) {
	if n > 0 {
		t.tx.Add(uint64(n))
	}
}

// OpenConn marks a forwarded connection as started.
func (t *Tunnel) OpenConn() {
	t.conns.Add(1)
	t.total.Add(1)
}

// CloseConn marks a forwarded connection as finished.
func (t *Tunnel) CloseConn() { t.conns.Add(-1) }

// SetLinked records whether a peer is currently connected.
func (t *Tunnel) SetLinked(v bool) { t.linked.Store(v) }

// SetPing records a measured round trip.
func (t *Tunnel) SetPing(d time.Duration) { t.pingMicros.Store(d.Microseconds()) }

// sample folds the current counters into the history ring. The registry calls
// this on a fixed interval.
func (t *Tunnel) sample(now time.Time) {
	rx, tx := t.rx.Load(), t.tx.Load()

	t.mu.Lock()
	defer t.mu.Unlock()

	var rxRate, txRate float64
	if !t.lastAt.IsZero() {
		if elapsed := now.Sub(t.lastAt).Seconds(); elapsed > 0 {
			if rx >= t.lastRx {
				rxRate = float64(rx-t.lastRx) / elapsed
			}
			if tx >= t.lastTx {
				txRate = float64(tx-t.lastTx) / elapsed
			}
		}
	}
	t.lastRx, t.lastTx, t.lastAt = rx, tx, now

	t.history = append(t.history, Point{At: now, RxRate: rxRate, TxRate: txRate})
	if len(t.history) > historyLen {
		t.history = t.history[len(t.history)-historyLen:]
	}
}

// Snapshot returns the tunnel's current state.
func (t *Tunnel) Snapshot() Snapshot {
	rx, tx := t.rx.Load(), t.tx.Load()

	t.mu.Lock()
	hist := make([]Point, len(t.history))
	copy(hist, t.history)
	started := t.startedA
	t.mu.Unlock()

	var rxRate, txRate float64
	if n := len(hist); n > 0 {
		rxRate, txRate = hist[n-1].RxRate, hist[n-1].TxRate
	}

	ping := -1.0
	if us := t.pingMicros.Load(); us > 0 {
		ping = float64(us) / 1000
	}
	var uptime int64
	if !started.IsZero() {
		uptime = int64(time.Since(started).Seconds())
	}

	return Snapshot{
		Name:      t.Name,
		Role:      t.Role,
		Transport: t.Transport,
		Port:      t.Port,
		Forwarded: t.Forwarded,
		Linked:    t.linked.Load(),
		RxBytes:   rx,
		TxBytes:   tx,
		Total:     rx + tx,
		RxRate:    rxRate,
		TxRate:    txRate,
		Conns:     t.conns.Load(),
		TotalConn: t.total.Load(),
		PingMs:    ping,
		Uptime:    uptime,
		History:   hist,
	}
}

// Registry holds every tunnel a process knows about and drives their sampling.
type Registry struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
	order   []string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tunnels: map[string]*Tunnel{}}
}

// Register adds a tunnel, or returns the existing one with that name.
func (r *Registry) Register(name, role, transport string, port int, forwarded []int) *Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tunnels[name]; ok {
		return t
	}
	t := &Tunnel{
		Name:      name,
		Role:      role,
		Transport: transport,
		Port:      port,
		Forwarded: forwarded,
		startedA:  time.Now(),
	}
	r.tunnels[name] = t
	r.order = append(r.order, name)
	return t
}

// Get returns a tunnel by name.
func (r *Registry) Get(name string) (*Tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tunnels[name]
	return t, ok
}

// Snapshots returns every tunnel's state, in registration order.
func (r *Registry) Snapshots() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tunnels[name]; ok {
			out = append(out, t.Snapshot())
		}
	}
	return out
}

// SampleInterval is how often the registry folds counters into history.
const SampleInterval = 10 * time.Second

// Run samples every registered tunnel until ctx is done. It is what gives the
// panel its rate figures and its sparklines.
func (r *Registry) Run(done <-chan struct{}) {
	t := time.NewTicker(SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			r.mu.RLock()
			tunnels := make([]*Tunnel, 0, len(r.tunnels))
			for _, tu := range r.tunnels {
				tunnels = append(tunnels, tu)
			}
			r.mu.RUnlock()
			for _, tu := range tunnels {
				tu.sample(now)
			}
		}
	}
}
