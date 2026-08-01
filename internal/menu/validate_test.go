package menu

import "testing"

func TestValidTunnelName(t *testing.T) {
	ok := []string{"main", "iran-1", "edge_2", "A0"}
	bad := []string{"has space", "slash/name", "dot.name", "semi;colon"}
	for _, s := range ok {
		if err := validTunnelName(s); err != nil {
			t.Errorf("validTunnelName(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range bad {
		if err := validTunnelName(s); err == nil {
			t.Errorf("validTunnelName(%q) accepted an unsafe name", s)
		}
	}
	// 64 is the ceiling; one past it is rejected.
	if err := validTunnelName(repeat("a", 65)); err == nil {
		t.Error("validTunnelName accepted a 65-character name")
	}
}

func TestValidIPAddr(t *testing.T) {
	for _, s := range []string{"10.200.0.1", "192.168.1.1", "::1", "fd00::2"} {
		if err := validIPAddr(s); err != nil {
			t.Errorf("validIPAddr(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", "10.200.0", "not-an-ip", "10.200.0.1:6060"} {
		if err := validIPAddr(s); err == nil {
			t.Errorf("validIPAddr(%q) accepted a non-IP", s)
		}
	}
}

func TestValidHostPort(t *testing.T) {
	for _, s := range []string{"203.0.113.5:6060", "example.com:443", "[::1]:22"} {
		if err := validHostPort(s); err != nil {
			t.Errorf("validHostPort(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", "example.com", "host:0", "host:70000", ":6060"} {
		if err := validHostPort(s); err == nil {
			t.Errorf("validHostPort(%q) accepted a bad address", s)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
