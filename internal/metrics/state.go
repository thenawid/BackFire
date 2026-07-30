package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Every tunnel runs as its own systemd unit, so the panel and the bot are
// separate processes from the engines whose traffic they report. They cannot
// read another process's counters directly.
//
// The bridge is a state file per tunnel: each engine publishes its snapshot to
// StateDir on the sampling interval, and the panel and the bot simply read the
// directory. StateDir lives under /run, which is a tmpfs — so a reboot clears
// it and a tunnel that is no longer running leaves nothing behind to
// misreport.
const (
	// StateDir is where engines publish their snapshots.
	StateDir = "/run/backfire"
	// staleAfter is how old a state file may be before its tunnel is treated as
	// not running. Comfortably more than the sampling interval so an engine that
	// is merely busy is not declared dead.
	staleAfter = 3 * SampleInterval
)

// State is what an engine publishes and the readers consume.
type State struct {
	Snapshot
	// PublishedAt lets a reader tell a live tunnel from a stale file.
	PublishedAt time.Time `json:"published_at"`
	// PID of the engine, for diagnostics.
	PID int `json:"pid"`
}

// statePath returns the file a named tunnel publishes to.
func statePath(name string) string {
	return filepath.Join(StateDir, name+".json")
}

// Publish writes one tunnel's snapshot atomically, so a reader never sees a
// half-written file.
func Publish(name string, s Snapshot) error {
	if err := os.MkdirAll(StateDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(State{Snapshot: s, PublishedAt: time.Now(), PID: os.Getpid()})
	if err != nil {
		return err
	}
	path := statePath(name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Unpublish removes a tunnel's state file, called when an engine shuts down
// cleanly so the panel stops showing it immediately.
func Unpublish(name string) { _ = os.Remove(statePath(name)) }

// PublishLoop keeps a tunnel's state file current until done is closed, then
// removes it.
func (t *Tunnel) PublishLoop(done <-chan struct{}) {
	tick := time.NewTicker(SampleInterval)
	defer tick.Stop()
	defer Unpublish(t.Name)

	_ = Publish(t.Name, t.Snapshot())
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			_ = Publish(t.Name, t.Snapshot())
		}
	}
}

// ReadAll returns the state of every tunnel that has published recently, sorted
// by name. Stale files are skipped rather than reported as live.
func ReadAll() []State {
	entries, err := os.ReadDir(StateDir)
	if err != nil {
		return nil
	}
	var out []State
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(StateDir, e.Name()))
		if err != nil {
			continue
		}
		var s State
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if time.Since(s.PublishedAt) > staleAfter {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Totals sums traffic across a set of tunnel states, for the panel's headline
// figures.
func Totals(states []State) (rx, tx uint64, rxRate, txRate float64, linked int) {
	for _, s := range states {
		rx += s.RxBytes
		tx += s.TxBytes
		rxRate += s.RxRate
		txRate += s.TxRate
		if s.Linked {
			linked++
		}
	}
	return
}
