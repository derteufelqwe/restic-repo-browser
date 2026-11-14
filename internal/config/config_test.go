package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"restic-repo-browser/internal/restic"
)

func TestParse(t *testing.T) {
	cfg, err := Parse(nil, io.Discard)
	if err != nil || cfg.Command != CmdServe || cfg.Listen != "127.0.0.1:8080" || cfg.DataDir != "data" || cfg.LogLevel != restic.LogDefault {
		t.Errorf("defaults: cfg = %+v, err = %v", cfg, err)
	}
	cfg, err = Parse([]string{"set-password", "--data-dir", "/x"}, io.Discard)
	if err != nil || cfg.Command != CmdSetPassword || cfg.DataDir != "/x" {
		t.Errorf("command: cfg = %+v, err = %v", cfg, err)
	}
	cfg, err = Parse([]string{"--listen", ":9000", "--debug", "--log-level", "responses"}, io.Discard)
	if err != nil || cfg.Listen != ":9000" || !cfg.Debug || cfg.LogLevel != restic.LogResponses {
		t.Errorf("flags: cfg = %+v, err = %v", cfg, err)
	}

	if _, err := Parse([]string{"--log-level", "verbose"}, io.Discard); err == nil {
		t.Error("an unknown log level must fail")
	}
	if _, err := Parse([]string{"frobnicate"}, io.Discard); err == nil {
		t.Error("unknown command must fail")
	}
	if _, err := Parse([]string{"serve", "extra"}, io.Discard); err == nil {
		t.Error("stray argument must fail")
	}
	if _, err := Parse([]string{"-h"}, io.Discard); !errors.Is(err, ErrHelp) {
		t.Errorf("-h: err = %v", err)
	}
}

func TestResolveRestic(t *testing.T) {
	if _, err := ResolveRestic("/definitely/not/here/restic"); err == nil {
		t.Error("expected an error for a missing explicit path")
	}

	// An explicit name is looked up on PATH, not only checked as a file.
	dir := t.TempDir()
	name := "restic-custom"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got, err := ResolveRestic("restic-custom"); err != nil || filepath.Base(got) != name {
		t.Errorf("name on PATH: got %q, err = %v", got, err)
	}
}

func TestResolveBrowseDir(t *testing.T) {
	if got, err := ResolveBrowseDir(""); err != nil || got != "" {
		t.Errorf("empty = %q, %v", got, err)
	}
	dir := t.TempDir()
	if got, err := ResolveBrowseDir(dir); err != nil || got != dir {
		t.Errorf("existing = %q, %v", got, err)
	}
	if _, err := ResolveBrowseDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing folder must be an error")
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBrowseDir(file); err == nil {
		t.Error("a file must be an error")
	}
	cfg, err := Parse([]string{"--browse-dir", "/srv/restic"}, io.Discard)
	if err != nil || cfg.BrowseDir != "/srv/restic" {
		t.Errorf("flag = %q, %v", cfg.BrowseDir, err)
	}
}
