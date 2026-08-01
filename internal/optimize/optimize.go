// Package optimize applies advanced network tuning to a VPS so a tunnel or VPN
// server can push the most bandwidth possible: BBR congestion control, large
// socket buffers sized for a high bandwidth-delay-product intercontinental link,
// aggressive TCP settings, IP forwarding for tunnels, and raised file-descriptor
// limits.
//
// Everything is written to drop-in files under /etc so it survives reboots and
// can be reverted by deleting one file, and the content builders are pure so the
// exact tuning is unit-tested without touching the host.
package optimize

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	sysctlPath = "/etc/sysctl.d/99-backfire.conf"
	limitsPath = "/etc/security/limits.d/99-backfire.conf"
)

// SysctlConf is the kernel tuning backfire applies. The values target a
// high-throughput tunnel server on a long fat network, not a laptop.
func SysctlConf() string {
	return `# backfire network optimization — safe to delete to revert.
#
# Congestion control: BBR with fair queueing gives the best throughput on a
# lossy or high-latency intercontinental path, where the classic loss-based
# controllers collapse their window on the first drop.
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr

# Socket buffers sized for a high bandwidth-delay product, so a single flow can
# actually fill a fast, distant link instead of stalling on a small window.
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864
net.core.rmem_default = 26214400
net.core.wmem_default = 26214400
net.core.optmem_max = 65536
net.core.netdev_max_backlog = 250000
net.core.somaxconn = 65535
net.ipv4.tcp_rmem = 4096 87380 67108864
net.ipv4.tcp_wmem = 4096 65536 67108864

# UDP buffers for the udp/kcp transports.
net.ipv4.udp_rmem_min = 8192
net.ipv4.udp_wmem_min = 8192

# TCP behaviour tuned for many long-lived tunnel connections.
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_notsent_lowat = 16384
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 5
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_max_tw_buckets = 1440000
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_window_scaling = 1
net.ipv4.tcp_sack = 1
net.ipv4.tcp_timestamps = 1
net.ipv4.ip_local_port_range = 1024 65535

# Forwarding, so the box can route tunnelled traffic.
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1

# Bigger connection-tracking table for a NAT-heavy tunnel server.
net.netfilter.nf_conntrack_max = 1048576

# Room for a very large number of open sockets.
fs.file-max = 2097152
fs.nr_open = 2097152
`
}

// LimitsConf raises the open-file limit for every user, so a busy tunnel server
// is not capped at the default 1024 descriptors.
func LimitsConf() string {
	return `# backfire — raised file-descriptor limits. Safe to delete to revert.
*     soft nofile 1048576
*     hard nofile 1048576
root  soft nofile 1048576
root  hard nofile 1048576
`
}

// Report describes what Apply did.
type Report struct {
	// Applied lists the steps that succeeded.
	Applied []string
	// Warnings lists non-fatal problems (e.g. BBR unavailable on this kernel).
	Warnings []string
	// RebootRecommended is true when some change only fully takes effect after a
	// reboot (the raised descriptor limits, mainly).
	RebootRecommended bool
}

// Apply writes the tuning files, loads the BBR module, and applies the sysctl
// settings live. It needs root.
func Apply() (*Report, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("optimization needs root")
	}
	rep := &Report{RebootRecommended: true}

	// BBR needs its module; loading it before sysctl means the congestion-control
	// setting is accepted rather than silently rejected.
	if bbrAvailable() {
		rep.Applied = append(rep.Applied, "BBR is available")
	} else {
		_ = exec.Command("modprobe", "tcp_bbr").Run()
		if bbrAvailable() {
			rep.Applied = append(rep.Applied, "loaded the tcp_bbr module")
		} else {
			rep.Warnings = append(rep.Warnings,
				"BBR is not available on this kernel; keeping the current congestion control")
		}
	}

	if err := writeFile(sysctlPath, SysctlConf(), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", sysctlPath, err)
	}
	rep.Applied = append(rep.Applied, "wrote "+sysctlPath)

	if err := writeFile(limitsPath, LimitsConf(), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", limitsPath, err)
	}
	rep.Applied = append(rep.Applied, "wrote "+limitsPath)

	// Apply the sysctl settings now so most of the tuning is live immediately,
	// no reboot required for it.
	out, err := exec.Command("sysctl", "--system").CombinedOutput()
	if err != nil {
		rep.Warnings = append(rep.Warnings,
			"sysctl --system reported errors (some keys may not exist on this kernel): "+
				strings.TrimSpace(lastLine(string(out))))
	} else {
		rep.Applied = append(rep.Applied, "applied sysctl settings live")
	}

	// Confirm BBR actually took, so the report does not overpromise.
	if cur := currentCongestion(); cur != "" {
		rep.Applied = append(rep.Applied, "congestion control is now "+cur)
	}
	return rep, nil
}

// bbrAvailable reports whether the kernel offers BBR.
func bbrAvailable() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv4/tcp_available_congestion_control")
	return err == nil && strings.Contains(string(b), "bbr")
}

// currentCongestion returns the active congestion-control algorithm.
func currentCongestion() string {
	b, err := os.ReadFile("/proc/sys/net/ipv4/tcp_congestion_control")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dir(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
