package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"restic-repo-browser/internal/restic"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil, io.Discard)
	if err != nil || cfg.Command != CmdServe || cfg.Listen != "127.0.0.1:8080" || cfg.DataDir != "data" {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestParseCommandAndFlags(t *testing.T) {
	cfg, err := Parse([]string{"set-password", "--data-dir", "/x"}, io.Discard)
	if err != nil || cfg.Command != CmdSetPassword || cfg.DataDir != "/x" {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
	cfg, err = Parse([]string{"--listen", ":9000", "--debug"}, io.Discard)
	if err != nil || cfg.Command != CmdServe || cfg.Listen != ":9000" || !cfg.Debug {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestParseLogLevel(t *testing.T) {
	cfg, err := Parse(nil, io.Discard)
	if err != nil || cfg.LogLevel != restic.LogDefault {
		t.Errorf("default: cfg.LogLevel = %v, err = %v", cfg.LogLevel, err)
	}
	for arg, want := range map[string]restic.LogMode{"commands": restic.LogCommands, "responses": restic.LogResponses, "default": restic.LogDefault} {
		cfg, err := Parse([]string{"--log-level", arg}, io.Discard)
		if err != nil || cfg.LogLevel != want {
			t.Errorf("--log-level %s: got %v, err = %v", arg, cfg.LogLevel, err)
		}
	}
	if _, err := Parse([]string{"--log-level", "verbose"}, io.Discard); err == nil || !strings.Contains(err.Error(), "default, commands or responses") {
		t.Errorf("invalid level: err = %v", err)
	}
}

func TestParseErrors(t *testing.T) {
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

func TestResolveResticExplicitMissing(t *testing.T) {
	if _, err := ResolveRestic("/definitely/not/here/restic"); err == nil {
		t.Error("expected an error for a missing explicit path")
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
