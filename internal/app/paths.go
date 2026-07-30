// Package app holds shared constants, filesystem paths and the top-level
// engine dispatch used across the backfire management layer.
package app

const (
	// Version is the backfire engine version.
	Version = "v0.1.0"

	// RepoOwner / RepoName identify the GitHub repository, used by the
	// installer and any future release-based updater.
	RepoOwner = "thenawid"
	RepoName  = "backfire"

	// ConfigDir is where per-tunnel TOML configs live.
	ConfigDir = "/etc/backfire"

	// ServiceDir is the systemd unit directory.
	ServiceDir = "/etc/systemd/system"

	// ServicePrefix is prepended to every tunnel's systemd unit.
	ServicePrefix = "backfire-"

	// BinPath is where the backfire binary is installed.
	BinPath = "/usr/local/bin/backfire"
)

// ServiceName returns the systemd unit name for a tunnel by its short name.
func ServiceName(name string) string {
	return ServicePrefix + name + ".service"
}

// ConfigPath returns the on-disk TOML path for a tunnel by its short name.
func ConfigPath(name string) string {
	return ConfigDir + "/" + name + ".toml"
}
