// Package backup builds and restores a snapshot of everything under the config
// directory: every tunnel's TOML plus the panel and bot settings. It is shared
// by the Telegram bot (which sends a backup) and the web panel (which can send
// one and restore one).
//
// A backup is a gzipped tar of flat files — the config directory has no
// subdirectories — so restoring it is just writing those files back and
// re-creating the systemd units for the tunnels they describe.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thenawid/backfire/internal/app"
)

// maxEntry caps a single restored file, and maxTotal the whole archive, so a
// hostile upload cannot exhaust disk or memory.
const (
	maxEntry = 1 << 20  // 1 MiB per config file is already generous
	maxTotal = 16 << 20 // 16 MiB for the whole archive
)

// Build produces a gzipped tar of every file in the config directory.
func Build() ([]byte, error) {
	entries, err := os.ReadDir(app.ConfigDir)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(app.ConfigDir, e.Name()))
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		hdr := &tar.Header{
			Name:    e.Name(),
			Mode:    int64(info.Mode().Perm()),
			Size:    int64(len(content)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Result reports what a restore wrote.
type Result struct {
	// Tunnels are the short names of the tunnel configs restored.
	Tunnels []string
	// HasWebUI / HasBot record whether panel and bot settings were in the
	// archive, so the caller can decide whether to re-create those services.
	HasWebUI bool
	HasBot   bool
	// Files is the total count of files written.
	Files int
}

// Extract writes an archive's files back into the config directory and reports
// what it found. It refuses any entry whose name is not a plain filename, so a
// crafted archive cannot use "../" to escape the config directory or plant a
// file elsewhere on disk — the classic tar traversal.
func Extract(data []byte) (*Result, error) {
	if len(data) > maxTotal {
		return nil, fmt.Errorf("backup is too large (%d bytes)", len(data))
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(app.ConfigDir, 0o755); err != nil {
		return nil, err
	}

	tr := tar.NewReader(io.LimitReader(gz, maxTotal))
	res := &Result{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := hdr.Name
		// The only safe entry is a bare filename in the config directory.
		if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("unsafe entry name in backup: %q", name)
		}
		if hdr.Size > maxEntry {
			return nil, fmt.Errorf("entry %q is too large (%d bytes)", name, hdr.Size)
		}

		content, err := io.ReadAll(io.LimitReader(tr, maxEntry+1))
		if err != nil {
			return nil, err
		}
		if int64(len(content)) > maxEntry {
			return nil, fmt.Errorf("entry %q exceeds the size limit", name)
		}

		// Config files carry secrets, so restore them owner-only.
		if err := os.WriteFile(filepath.Join(app.ConfigDir, name), content, 0o600); err != nil {
			return nil, err
		}
		res.Files++

		switch {
		case strings.HasSuffix(name, ".toml"):
			res.Tunnels = append(res.Tunnels, strings.TrimSuffix(name, ".toml"))
		case name == "webui.json":
			res.HasWebUI = true
		case name == "telegram.json":
			res.HasBot = true
		}
	}
	sort.Strings(res.Tunnels)
	return res, nil
}
