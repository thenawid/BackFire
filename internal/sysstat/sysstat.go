// Package sysstat reads host vitals — processor, memory, swap, disk, uptime and
// distribution — straight from /proc and statfs.
//
// Doing it by hand rather than pulling in a portability library keeps the
// dependency tree small, and costs nothing here: backfire runs on Linux VPSes,
// which is exactly where these interfaces are stable.
package sysstat

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Stats is one snapshot of the host.
type Stats struct {
	// CPUPercent is utilisation since the previous sample, 0-100.
	CPUPercent float64 `json:"cpu_percent"`
	Cores      int     `json:"cores"`

	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	MemPercent float64 `json:"mem_percent"`

	SwapUsed    uint64  `json:"swap_used"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapPercent float64 `json:"swap_percent"`

	DiskUsed    uint64  `json:"disk_used"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskPercent float64 `json:"disk_percent"`

	// Uptime is how long the host has been up.
	Uptime time.Duration `json:"-"`
	// UptimeSeconds is the same value, for JSON consumers.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// OS is the pretty distribution name, e.g. "Ubuntu 24.04".
	OS string `json:"os"`

	// NetRxBytes / NetTxBytes are cumulative host-wide interface counters.
	NetRxBytes uint64 `json:"net_rx_bytes"`
	NetTxBytes uint64 `json:"net_tx_bytes"`
	// NetRxRate / NetTxRate are bytes per second since the previous sample.
	NetRxRate float64 `json:"net_rx_rate"`
	NetTxRate float64 `json:"net_tx_rate"`
}

// cpuTimes is the aggregate jiffy counter split into busy and total.
type cpuTimes struct{ busy, total uint64 }

// Collector samples the host. Rates need two points in time, so a Collector
// remembers the previous sample; call Sample on a fixed interval.
type Collector struct {
	mu       sync.Mutex
	lastCPU  cpuTimes
	lastNet  [2]uint64 // rx, tx
	lastTime time.Time
	// diskPath is the filesystem whose usage is reported.
	diskPath string
}

// NewCollector builds a Collector reporting disk usage for the given path
// (empty means "/").
func NewCollector(diskPath string) *Collector {
	if diskPath == "" {
		diskPath = "/"
	}
	return &Collector{diskPath: diskPath}
}

// Sample returns the current host vitals. The first call reports zero for the
// rate-based fields, since a rate needs a previous point to compare against.
func (c *Collector) Sample() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	s := Stats{Cores: runtime.NumCPU(), OS: osName()}

	// CPU — the difference in busy jiffies over the difference in total.
	if cur, err := readCPUTimes(); err == nil {
		if c.lastCPU.total > 0 && cur.total > c.lastCPU.total {
			dBusy := float64(cur.busy - c.lastCPU.busy)
			dTotal := float64(cur.total - c.lastCPU.total)
			s.CPUPercent = clampPercent(dBusy / dTotal * 100)
		}
		c.lastCPU = cur
	}

	// Memory and swap.
	if m, err := readMemInfo(); err == nil {
		s.MemTotal = m.memTotal
		s.MemUsed = m.memUsed
		s.MemPercent = percent(m.memUsed, m.memTotal)
		s.SwapTotal = m.swapTotal
		s.SwapUsed = m.swapUsed
		s.SwapPercent = percent(m.swapUsed, m.swapTotal)
	}

	// Disk.
	if total, free, err := diskUsage(c.diskPath); err == nil {
		s.DiskTotal = total
		s.DiskUsed = total - free
		s.DiskPercent = percent(s.DiskUsed, total)
	}

	// Uptime.
	if up, err := readUptime(); err == nil {
		s.Uptime = up
		s.UptimeSeconds = int64(up.Seconds())
	}

	// Network throughput across all real interfaces.
	if rx, tx, err := readNetBytes(); err == nil {
		s.NetRxBytes, s.NetTxBytes = rx, tx
		if !c.lastTime.IsZero() {
			if elapsed := now.Sub(c.lastTime).Seconds(); elapsed > 0 {
				if rx >= c.lastNet[0] {
					s.NetRxRate = float64(rx-c.lastNet[0]) / elapsed
				}
				if tx >= c.lastNet[1] {
					s.NetTxRate = float64(tx-c.lastNet[1]) / elapsed
				}
			}
		}
		c.lastNet = [2]uint64{rx, tx}
	}
	c.lastTime = now
	return s
}

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var total, idle uint64
		for i, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			total += v
			// Fields 3 and 4 (idle, iowait) are the not-busy time.
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return cpuTimes{busy: total - idle, total: total}, nil
	}
	return cpuTimes{}, fmt.Errorf("no cpu line in /proc/stat")
}

type memInfo struct {
	memTotal, memUsed   uint64
	swapTotal, swapUsed uint64
}

func readMemInfo() (memInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()

	vals := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		// Values are in kB.
		if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			vals[key] = v * 1024
		}
	}

	m := memInfo{memTotal: vals["MemTotal"], swapTotal: vals["SwapTotal"]}
	// MemAvailable is the kernel's own estimate of what a new workload could
	// claim, which is a truer "used" than subtracting MemFree alone.
	if avail, ok := vals["MemAvailable"]; ok && m.memTotal >= avail {
		m.memUsed = m.memTotal - avail
	} else {
		m.memUsed = m.memTotal - vals["MemFree"] - vals["Buffers"] - vals["Cached"]
	}
	if m.swapTotal >= vals["SwapFree"] {
		m.swapUsed = m.swapTotal - vals["SwapFree"]
	}
	return m, nil
}

func diskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	// Bavail, not Bfree: the reserved-for-root blocks are not usable space.
	return st.Blocks * bs, st.Bavail * bs, nil
}

func readUptime() (time.Duration, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty /proc/uptime")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// readNetBytes totals receive and transmit counters across every interface that
// is not loopback or virtual, so the figure reflects real uplink traffic.
func readNetBytes() (rx, tx uint64, err error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if skipInterface(name) {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(fields[0], 10, 64)
		t, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 == nil && err2 == nil {
			rx += r
			tx += t
		}
	}
	return rx, tx, nil
}

// skipInterface filters out loopback and the usual virtual interfaces, whose
// traffic is not uplink traffic and would double-count a tunnel.
func skipInterface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, prefix := range []string{"docker", "veth", "br-", "virbr", "tun", "tap", "wg"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// osName returns the distribution's pretty name, falling back to the kernel's
// idea of the platform.
func osName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampPercent(float64(used) / float64(total) * 100)
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// FormatBytes renders a byte count the way the panel and the bot show it.
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// FormatRate renders a bytes-per-second figure.
func FormatRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	return FormatBytes(uint64(bps)) + "/s"
}

// FormatUptime renders a duration as "19h 34m" / "3d 4h".
func FormatUptime(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
