package config

import "testing"

func TestParseForward(t *testing.T) {
	cases := []struct {
		in         string
		wantListen string
		wantTarget string
		wantErr    bool
	}{
		{"80=127.0.0.1:80", "0.0.0.0:80", "127.0.0.1:80", false},
		{"0.0.0.0:2222=127.0.0.1:22", "0.0.0.0:2222", "127.0.0.1:22", false},
		{"443", "0.0.0.0:443", "127.0.0.1:443", false},
		{"8080=9090", "0.0.0.0:8080", "127.0.0.1:9090", false},
		{"1.2.3.4:80=example:443", "1.2.3.4:80", "example:443", false},
		{"", "", "", true},
		{"notaport", "", "", true},
		{"80=notaport:x", "", "", true},
	}
	for _, c := range cases {
		f, err := ParseForward(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseForward(%q): expected error, got %+v", c.in, f)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseForward(%q): unexpected error %v", c.in, err)
			continue
		}
		if f.Listen != c.wantListen || f.Target != c.wantTarget {
			t.Errorf("ParseForward(%q) = {%s %s}, want {%s %s}",
				c.in, f.Listen, f.Target, c.wantListen, c.wantTarget)
		}
	}
}

// TestTransportSplit pins the base/mode decomposition of every transport — the
// table the whole nine-transport design rests on.
func TestTransportSplit(t *testing.T) {
	want := map[Transport]struct {
		base Base
		mode Mode
	}{
		TCP:     {BaseTCP, ModePool},
		TCPMUX:  {BaseTCP, ModeMux},
		STEALTH: {BaseStealth, ModeMux},
		UDP:     {BaseUDP, ModeMux},
		KCP:     {BaseKCP, ModeMux},
		WS:      {BaseWS, ModePool},
		WSMUX:   {BaseWS, ModeMux},
		WSS:     {BaseWSS, ModePool},
		WSSMUX:  {BaseWSS, ModeMux},
	}
	if len(want) != len(KnownTransports) {
		t.Fatalf("table covers %d transports but KnownTransports has %d",
			len(want), len(KnownTransports))
	}
	for _, tr := range KnownTransports {
		exp, ok := want[tr]
		if !ok {
			t.Errorf("KnownTransports has %q with no expectation in the table", tr)
			continue
		}
		base, mode := tr.Split()
		if base != exp.base || mode != exp.mode {
			t.Errorf("%s splits to (%s, %s), want (%s, %s)", tr, base, mode, exp.base, exp.mode)
		}
		if tr.IsMux() != (exp.mode == ModeMux) {
			t.Errorf("%s: IsMux disagrees with mode %s", tr, mode)
		}
		if tr.Describe() == "unknown" {
			t.Errorf("%s has no description", tr)
		}
	}
}

func TestUnknownTransportSplitsEmpty(t *testing.T) {
	base, mode := Transport("nonsense").Split()
	if base != "" || mode != "" {
		t.Errorf("unknown transport split to (%q, %q), want empty", base, mode)
	}
}

func TestValidateRejectsUnknownTransport(t *testing.T) {
	c := &Config{
		Role: RoleServer,
		Server: ServerConfig{
			Bind:      "0.0.0.0:6060",
			Transport: "carrier-pigeon",
			Token:     "secret",
			Forwards:  []string{"80"},
		},
	}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error for an unknown transport")
	}
}

// TestKCPDefaultsDisableHalfConfiguredFEC covers the trap where parity is set
// without data shards: the encoder cannot use that, so it must read as FEC off
// rather than starting a broken session.
func TestKCPDefaultsDisableHalfConfiguredFEC(t *testing.T) {
	k := KCPConfig{ParityShards: 3}.withDefaults()
	if k.DataShards != 0 || k.ParityShards != 0 {
		t.Errorf("half-configured FEC became %d:%d, want 0:0", k.DataShards, k.ParityShards)
	}
	if k.MTU <= 0 || k.SndWnd <= 0 || k.RcvWnd <= 0 || k.Interval <= 0 {
		t.Error("KCP defaults left a zero value that would break a session")
	}
}

func TestPoolDefaults(t *testing.T) {
	p := PoolConfig{}.withDefaults()
	if p.Size <= 0 || p.IdleTimeout <= 0 {
		t.Fatalf("pool defaults are unusable: %+v", p)
	}
	if got := (PoolConfig{Size: 10000}).withDefaults().Size; got > 512 {
		t.Errorf("pool size %d was not clamped", got)
	}
}

func TestValidateRejectsBadRole(t *testing.T) {
	c := &Config{Role: "bogus"}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestServerValidateNeedsForwards(t *testing.T) {
	c := &Config{
		Role: RoleServer,
		Server: ServerConfig{
			Bind:      "0.0.0.0:6060",
			Transport: TCP,
			Token:     "secret",
		},
	}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty forwards")
	}
}
