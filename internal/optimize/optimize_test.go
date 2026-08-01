package optimize

import (
	"strings"
	"testing"
)

// TestSysctlConfHasKeyTuning guards the specific settings the optimization is
// about, so an accidental edit that drops BBR or the big buffers is caught.
func TestSysctlConfHasKeyTuning(t *testing.T) {
	conf := SysctlConf()
	must := []string{
		"tcp_congestion_control = bbr",
		"default_qdisc = fq",
		"net.core.rmem_max = 67108864",
		"net.core.wmem_max = 67108864",
		"tcp_fastopen = 3",
		"tcp_slow_start_after_idle = 0",
		"net.ipv4.ip_forward = 1",
		"fs.file-max",
	}
	for _, m := range must {
		if !strings.Contains(conf, m) {
			t.Errorf("sysctl config is missing %q", m)
		}
	}
}

// TestSysctlConfParses checks every non-comment line is a "key = value" pair, so
// the file `sysctl --system` reads is never malformed.
func TestSysctlConfParses(t *testing.T) {
	for i, line := range strings.Split(SysctlConf(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			t.Errorf("line %d is not a key=value sysctl setting: %q", i+1, line)
		}
	}
}

func TestLimitsConfRaisesNofile(t *testing.T) {
	conf := LimitsConf()
	if !strings.Contains(conf, "nofile 1048576") {
		t.Error("limits config does not raise the open-file limit")
	}
	if !strings.Contains(conf, "* ") || !strings.Contains(conf, "root ") {
		t.Error("limits config should cover both all users and root")
	}
}
