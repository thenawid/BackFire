package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/thenawid/backfire/internal/app"
)

// withConfigDir points app.ConfigDir at a temp directory for the duration of a
// test. The constant is not writable, so the tests that need it are the reason
// ConfigDir is read through the variable indirection.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := app.ConfigDir
	app.ConfigDir = dir
	t.Cleanup(func() { app.ConfigDir = orig })
	return dir
}

func TestBuildAndExtractRoundTrip(t *testing.T) {
	dir := withConfigDir(t)
	files := map[string]string{
		"main.toml":     "family = \"backpack\"\nrole = \"server\"\n",
		"eu.toml":       "family = \"backhaul\"\n",
		"webui.json":    `{"port":7777}`,
		"telegram.json": `{"token":"x"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	data, err := Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Restore into a fresh, empty directory and check everything comes back.
	dir2 := withConfigDir(t)
	res, err := Extract(data)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Files != len(files) {
		t.Errorf("restored %d files, want %d", res.Files, len(files))
	}
	if !res.HasWebUI || !res.HasBot {
		t.Errorf("HasWebUI=%v HasBot=%v, want both true", res.HasWebUI, res.HasBot)
	}
	if len(res.Tunnels) != 2 || res.Tunnels[0] != "eu" || res.Tunnels[1] != "main" {
		t.Errorf("restored tunnels = %v, want [eu main]", res.Tunnels)
	}
	for name, body := range files {
		got, err := os.ReadFile(filepath.Join(dir2, name))
		if err != nil {
			t.Errorf("restored file %s missing: %v", name, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s content mismatch", name)
		}
		// Restored config files must be owner-only, they carry tokens.
		info, _ := os.Stat(filepath.Join(dir2, name))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

// TestExtractRejectsTraversal is the security property: an archive that tries to
// write outside the config directory with "../" must be refused, not obeyed.
func TestExtractRejectsTraversal(t *testing.T) {
	withConfigDir(t)
	for _, evil := range []string{"../escape.toml", "sub/dir.toml", "/etc/passwd"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		body := []byte("pwned")
		tw.WriteHeader(&tar.Header{Name: evil, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write(body)
		tw.Close()
		gz.Close()

		if _, err := Extract(buf.Bytes()); err == nil {
			t.Errorf("Extract accepted a traversal entry %q", evil)
		}
	}
}

func TestExtractRejectsNonGzip(t *testing.T) {
	withConfigDir(t)
	if _, err := Extract([]byte("this is not gzip")); err == nil {
		t.Error("expected an error for a non-gzip payload")
	}
}

// TestExtractRejectsHugeEntry guards the per-file cap.
func TestExtractRejectsHugeEntry(t *testing.T) {
	withConfigDir(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	big := make([]byte, maxEntry+1)
	tw.WriteHeader(&tar.Header{Name: "big.toml", Mode: 0o600, Size: int64(len(big)), Typeflag: tar.TypeReg})
	tw.Write(big)
	tw.Close()
	gz.Close()
	if _, err := Extract(buf.Bytes()); err == nil {
		t.Error("expected an error for an oversized entry")
	}
}
