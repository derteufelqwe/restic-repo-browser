package config

import (
	"errors"
	"io"
	"testing"
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
