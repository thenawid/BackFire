package updater

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in            string
		maj, min, pat int
		known         bool
	}{
		{"v0.6.0", 0, 6, 0, true},
		{"1.2.3", 1, 2, 3, true},
		{"v2", 2, 0, 0, true},
		{"v1.4.0-rc1", 1, 4, 0, true},
		{"", 0, 0, 0, false},
		{"unknown", 0, 0, 0, false},
		{"not-a-version", 0, 0, 0, false},
	}
	for _, c := range cases {
		v := Parse(c.in)
		if v.Known != c.known {
			t.Errorf("Parse(%q).Known = %v, want %v", c.in, v.Known, c.known)
		}
		if v.Known && (v.Major != c.maj || v.Minor != c.min || v.Patch != c.pat) {
			t.Errorf("Parse(%q) = %d.%d.%d, want %d.%d.%d",
				c.in, v.Major, v.Minor, v.Patch, c.maj, c.min, c.pat)
		}
	}
}

func TestCompareAndNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.2.0", "v1.1.9", 1},
		{"v1.0.0", "v2.0.0", -1},
		{"unknown", "v0.1.0", -1}, // unknown sorts below any known version
		{"v0.1.0", "unknown", 1},
		{"unknown", "unknown", 0},
	}
	for _, c := range cases {
		got := Compare(Parse(c.a), Parse(c.b))
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !Newer(Parse("v0.6.0"), Parse("v0.7.0")) {
		t.Error("v0.7.0 should be newer than v0.6.0")
	}
	if Newer(Parse("v0.7.0"), Parse("v0.6.0")) {
		t.Error("v0.6.0 is not newer than v0.7.0")
	}
}

// TestMuchOlder pins the rule the mutual-update warning uses.
func TestMuchOlder(t *testing.T) {
	self := Parse("v1.4.0")
	cases := []struct {
		peer string
		want bool
	}{
		{"v1.4.0", false}, // same
		{"v1.3.0", false}, // one minor behind — tolerated
		{"v1.2.0", true},  // two minors behind — warn
		{"v0.9.0", true},  // major behind — warn
		{"v2.0.0", false}, // peer is newer, not older
		{"unknown", true}, // predates version reporting — warn
	}
	for _, c := range cases {
		if got := MuchOlder(Parse(c.peer), self); got != c.want {
			t.Errorf("MuchOlder(%q, v1.4.0) = %v, want %v", c.peer, got, c.want)
		}
	}
	// An unknown local version should not warn about anything.
	if MuchOlder(Parse("v1.0.0"), Parse("")) {
		t.Error("an unknown local version should not raise the warning")
	}
}
