package restic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"restic-repo-browser/internal/backend"
)

// scriptRunner writes fixed output to both streams and returns err.
type scriptRunner struct {
	stdout, stderr string
	err            error
}

func (r scriptRunner) Run(_ context.Context, _, _ []string, stdout, stderr io.Writer) error {
	io.WriteString(stdout, r.stdout)
	io.WriteString(stderr, r.stderr)
	return r.err
}

// logged runs fn on a client logging in mode and returns the log text.
func logged(t *testing.T, mode LogMode, r Runner, fn func(c *Client)) string {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
	fn(NewWithRunner(r, "/cache", WithLogging(log, mode)))
	return buf.String()
}

func TestParseLogMode(t *testing.T) {
	for in, want := range map[string]LogMode{"": LogDefault, "default": LogDefault, "Commands": LogCommands, " responses ": LogResponses} {
		if got, err := ParseLogMode(in); err != nil || got != want {
			t.Errorf("ParseLogMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLogMode("verbose"); err == nil {
		t.Error("an unknown level must be refused")
	}
}

func TestCommandsModeLogsNeitherOutputNorPassword(t *testing.T) {
	ref := backend.Ref{Kind: "local", Location: "/srv/my repo", Password: "hunter2"}
	out := logged(t, LogCommands, scriptRunner{stdout: "SECRET-OUTPUT", stderr: "SECRET-ERR"}, func(c *Client) {
		c.List(context.Background(), ref, "abcd", "/my docs")
	})
	for _, want := range []string{"op=ls", `restic -r '/srv/my repo' ls --json --no-lock abcd '/my docs'`, "exit=0", "stdout_bytes=13"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, banned := range []string{"SECRET-OUTPUT", "SECRET-ERR", "hunter2"} {
		if strings.Contains(out, banned) {
			t.Errorf("log contains %q:\n%s", banned, out)
		}
	}
}

func TestResponsesAreTruncatedAndDumpDataIsNeverLogged(t *testing.T) {
	big := strings.Repeat("x", maxLoggedOutput*3)
	var got bytes.Buffer
	out := logged(t, LogResponses, scriptRunner{stdout: big}, func(c *Client) {
		c.Snapshots(context.Background(), fakeRef) // unparsable output, only the log matters here
		c.exec(context.Background(), fakeRef, &got, io.Discard, "ls")
	})
	if got.Len() != len(big) {
		t.Errorf("caller received %d bytes, want all %d", got.Len(), len(big))
	}
	if strings.Count(out, "x") > 2*(maxLoggedOutput+10) || !strings.Contains(out, "truncated") {
		t.Errorf("log not truncated as expected (len %d)", len(out))
	}

	out = logged(t, LogResponses, scriptRunner{stdout: "FILE-CONTENT", stderr: "Fatal: oops"}, func(c *Client) {
		c.Dump(context.Background(), fakeRef, "abcd", "/a.txt", false, io.Discard)
	})
	if strings.Contains(out, "FILE-CONTENT") || !strings.Contains(out, "not logged: file data") || !strings.Contains(out, "Fatal: oops") {
		t.Errorf("dump logged wrongly:\n%s", out)
	}
}

func TestFailuresAreLoggedAndCombinedWriterStaysShared(t *testing.T) {
	out := logged(t, LogCommands, scriptRunner{stderr: "Fatal: wrong password", err: exitCode(12)}, func(c *Client) {
		if err := c.Probe(context.Background(), fakeRef); !errors.Is(err, backend.ErrWrongPassword) {
			t.Errorf("err = %v", err)
		}
	})
	for _, want := range []string{"level=WARN", "restic failed", "exit=12"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// One writer for both streams (as prune uses) stays one writer, so os/exec
	// keeps copying through a single goroutine.
	var seen [2]io.Writer
	r := runnerFunc(func(stdout, stderr io.Writer) error {
		seen[0], seen[1] = stdout, stderr
		return nil
	})
	var combined bytes.Buffer
	logged(t, LogResponses, r, func(c *Client) {
		c.exec(context.Background(), fakeRef, &combined, &combined, "prune")
	})
	if seen[0] != seen[1] {
		t.Error("stdout and stderr were wrapped into different writers")
	}
}

type runnerFunc func(stdout, stderr io.Writer) error

func (f runnerFunc) Run(_ context.Context, _, _ []string, stdout, stderr io.Writer) error {
	return f(stdout, stderr)
}

func TestCommandLineQuoting(t *testing.T) {
	got := commandLine([]string{"-r", "C:\\Users\\me\\my repo", "ls", "it's", "", "plain/arg-1.json"})
	want := `restic -r 'C:\Users\me\my repo' ls 'it'\''s' '' plain/arg-1.json`
	if got != want {
		t.Errorf("commandLine = %s\nwant          %s", got, want)
	}
}
