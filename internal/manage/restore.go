package manage

import (
	"fmt"
	"strings"

	"github.com/thenawid/backfire/internal/backup"
)

// RestoreReport summarises what Restore did, for the panel and the bot to show.
type RestoreReport struct {
	Restored []string // tunnels re-installed and started
	Failed   []string // "name: reason" for any that could not be re-installed
	Bot      bool     // whether the Telegram bot service was re-installed
}

// Restore writes a backup's files back and re-creates the systemd units so the
// restored tunnels actually run, not just sit on disk.
//
// The panel is deliberately not restarted here: the caller is usually the
// running panel, and restarting its own service would kill the request before
// it could report success. Its settings file is restored and takes effect on
// the next restart.
func Restore(data []byte) (*RestoreReport, error) {
	res, err := backup.Extract(data)
	if err != nil {
		return nil, err
	}

	rep := &RestoreReport{}
	for _, name := range res.Tunnels {
		cfg, err := LoadConfig(name)
		if err != nil {
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		// Install rewrites the (already-restored) config, re-creates the unit,
		// enables and starts it — exactly what a fresh create does.
		if err := Install(name, cfg); err != nil {
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		rep.Restored = append(rep.Restored, name)
	}

	// A bot restart is safe — it is a different process — so bring it back if its
	// settings were part of the backup.
	if res.HasBot {
		if err := InstallBot(); err == nil {
			rep.Bot = true
		}
	}
	return rep, nil
}

// Summary renders a restore report as a short human line.
func (r *RestoreReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "restored %d tunnel(s)", len(r.Restored))
	if r.Bot {
		b.WriteString(", bot re-installed")
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "; %d failed", len(r.Failed))
	}
	return b.String()
}
