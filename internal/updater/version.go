// Package updater checks GitHub for a newer backfire release and installs it in
// place, without interrupting running tunnels.
//
// The safety guarantee is a property of how the binary is replaced: the new
// binary is renamed over the old path atomically, and a running process keeps
// the inode it was started from open, so every tunnel engine keeps running the
// version it started with until it is deliberately restarted. Updating the file
// therefore never drops a tunnel — the operator chooses when (or whether) to
// restart the engines onto the new version.
package updater

import (
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Missing components are zero.
type Version struct {
	Major, Minor, Patch int
	// Raw is the original string, e.g. "v0.6.0"; "" or "unknown" means the
	// version could not be determined (an older peer that predates version
	// reporting).
	Raw   string
	Known bool
}

// Parse turns "v1.2.3" (or "1.2.3") into a Version. An unparseable or empty
// string yields an unknown version rather than an error, since a missing peer
// version is expected, not exceptional.
func Parse(s string) Version {
	v := Version{Raw: s}
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if t == "" || t == "unknown" {
		return v
	}
	// Drop any pre-release/build suffix after the numeric core.
	if i := strings.IndexAny(t, "-+"); i >= 0 {
		t = t[:i]
	}
	parts := strings.Split(t, ".")
	nums := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return Version{Raw: s} // not a version we understand
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	v.Known = true
	return v
}

// Compare returns -1 if a < b, 0 if equal, +1 if a > b. Unknown versions sort
// below any known one.
func Compare(a, b Version) int {
	if a.Known != b.Known {
		if a.Known {
			return 1
		}
		return -1
	}
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Newer reports whether b is a newer version than a.
func Newer(a, b Version) bool { return Compare(a, b) < 0 }

// MuchOlder reports whether peer is far enough behind self that the two may no
// longer interoperate cleanly and the operator should update the peer too. A
// different major version, or two or more minor versions behind, counts — as
// does an unknown peer, which predates version reporting entirely.
func MuchOlder(peer, self Version) bool {
	if !peer.Known {
		return self.Known // an unknown peer next to a known local build is "old"
	}
	if peer.Major != self.Major {
		return peer.Major < self.Major
	}
	return self.Minor-peer.Minor >= 2
}
