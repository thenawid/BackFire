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
