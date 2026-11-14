package restic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
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
	if _, err := ParseLogMode("verbose"); err == nil || !strings.Contains(err.Error(), "default, commands or responses") {
		t.Errorf("unknown level error = %v", err)
	}
}

func TestDefaultModeLogsNothing(t *testing.T) {
	out := logged(t, LogDefault, scriptRunner{stdout: "{}"}, func(c *Client) {
		if err := c.Probe(context.Background(), Repo{Path: "/r", Password: "pw"}); err != nil {
			t.Fatal(err)
		}
	})
	if out != "" {
		t.Errorf("default mode logged: %s", out)
	}
}

func TestCommandsModeLogsCommandsButNoOutput(t *testing.T) {
	out := logged(t, LogCommands, scriptRunner{stdout: "SECRET-OUTPUT", stderr: "SECRET-ERR"}, func(c *Client) {
		if _, err := c.List(context.Background(), Repo{Path: "/srv/my repo", Password: "hunter2"}, "abcd", "/my docs"); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"restic command", "op=ls", `restic -r '/srv/my repo' ls --json --no-lock abcd '/my docs'`, "restic finished", "exit=0", "took=", "stdout_bytes=13", "stderr_bytes=10"} {
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

func TestResponsesModeLogsOutput(t *testing.T) {
	r := scriptRunner{stdout: `{"struct_type":"snapshot"}` + "\n", stderr: "warning: something\n"}
	out := logged(t, LogResponses, r, func(c *Client) {
		c.Snapshots(context.Background(), Repo{Path: "/r", Password: "hunter2"})
	})
	for _, want := range []string{"restic command", "restic finished", "struct_type", "warning: something"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("log contains the password:\n%s", out)
	}
}

func TestResponsesAreTruncated(t *testing.T) {
	big := strings.Repeat("x", maxLoggedOutput*3)
	var got bytes.Buffer
	out := logged(t, LogResponses, scriptRunner{stdout: big}, func(c *Client) {
		if err := c.stream(context.Background(), Repo{Path: "/r"}, &got, io.Discard, "ls"); err != nil {
			t.Fatal(err)
		}
	})
	if got.Len() != len(big) {
		t.Errorf("caller received %d bytes, want all %d", got.Len(), len(big))
	}
	if strings.Count(out, "x") > maxLoggedOutput+10 || !strings.Contains(out, "truncated") || !strings.Contains(out, "24576 bytes in total") {
		t.Errorf("log not truncated as expected (len %d): %.300s", len(out), out)
	}
}

func TestDumpDataIsNeverLogged(t *testing.T) {
	out := logged(t, LogResponses, scriptRunner{stdout: "FILE-CONTENT", stderr: "Fatal: oops"}, func(c *Client) {
		c.Dump(context.Background(), Repo{Path: "/r"}, "abcd", "/a.txt", false, io.Discard)
	})
	if strings.Contains(out, "FILE-CONTENT") {
		t.Errorf("file data in the log:\n%s", out)
	}
	for _, want := range []string{"op=dump", "not logged: file data", "stdout_bytes=12", "Fatal: oops"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFailuresAreLoggedWithTheirExitCode(t *testing.T) {
	out := logged(t, LogCommands, scriptRunner{stderr: "Fatal: wrong password", err: exitCode(12)}, func(c *Client) {
		if err := c.Probe(context.Background(), Repo{Path: "/r"}); err == nil {
			t.Fatal("expected an error")
		}
	})
	for _, want := range []string{"level=WARN", "restic failed", "exit=12"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out = logged(t, LogCommands, scriptRunner{err: errors.New("start failed")}, func(c *Client) {
		c.Probe(context.Background(), Repo{Path: "/r"})
	})
	if !strings.Contains(out, "exit=-1") || !strings.Contains(out, "start failed") {
		t.Errorf("non-exit error not logged:\n%s", out)
	}
}

// One writer for both streams (as a cleanup run uses) stays one writer, so os/exec
// keeps copying through a single goroutine.
func TestCombinedWriterStaysShared(t *testing.T) {
	var seen [2]io.Writer
	r := runnerFunc(func(stdout, stderr io.Writer) error {
		seen[0], seen[1] = stdout, stderr
		io.WriteString(stdout, "out;")
		io.WriteString(stderr, "err;")
		return nil
	})
	var combined bytes.Buffer
	out := logged(t, LogResponses, r, func(c *Client) {
		if err := c.stream(context.Background(), Repo{Path: "/r"}, &combined, &combined, "forget"); err != nil {
			t.Fatal(err)
		}
	})
	if seen[0] != seen[1] {
		t.Error("stdout and stderr were wrapped into different writers")
	}
	if combined.String() != "out;err;" {
		t.Errorf("combined = %q", combined.String())
	}
	if !strings.Contains(out, "output_bytes=8") || !strings.Contains(out, "out;err;") {
		t.Errorf("combined output not logged:\n%s", out)
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
	if subcommand([]string{"-r", "/x", "--quiet", "list", "locks"}) != "list" || subcommand([]string{"version"}) != "version" {
		t.Error("subcommand not found")
	}
}
