package restic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogMode says how much of the restic traffic is written to the log.
type LogMode int

// The available log modes.
const (
	// LogDefault logs nothing about restic calls (failures are reported by the callers).
	LogDefault LogMode = iota
	// LogCommands logs every restic command line and how it ended.
	LogCommands
	// LogResponses additionally logs what restic printed.
	LogResponses
)

// ParseLogMode converts the value of --log-level.
func ParseLogMode(s string) (LogMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default":
		return LogDefault, nil
	case "commands":
		return LogCommands, nil
	case "responses":
		return LogResponses, nil
	}
	return LogDefault, fmt.Errorf("unknown log level %q (use default, commands or responses)", s)
}

func (m LogMode) String() string {
	switch m {
	case LogCommands:
		return "commands"
	case LogResponses:
		return "responses"
	}
	return "default"
}

// Option customises a Client.
type Option func(*Client)

// WithLogging makes the client log its restic calls according to mode.
// LogDefault leaves the client untouched.
func WithLogging(log *slog.Logger, mode LogMode) Option {
	return func(c *Client) {
		if mode != LogDefault && log != nil {
			c.runner = &loggingRunner{inner: c.runner, log: log, mode: mode}
		}
	}
}

const (
	maxLoggedOutput = 8 << 10 // bytes of one output stream that reach the log
	fileDataNotice  = "(not logged: file data)"
)

// loggingRunner decorates a Runner with command and response logging.
// Environment variables (the repository password) are never logged.
type loggingRunner struct {
	inner Runner
	log   *slog.Logger
	mode  LogMode
}

func (l *loggingRunner) Run(ctx context.Context, env, args []string, stdout, stderr io.Writer) error {
	op := subcommand(args)
	l.log.Info("restic command", "op", op, "cmd", commandLine(args))

	// A caller may pass one writer for both streams (os/exec then copies both
	// through a single goroutine). Keep that property: wrap it only once.
	combined := stdout != nil && sameWriter(stdout, stderr)
	outCap := newCapWriter(stdout, maxLoggedOutput)
	errCap := outCap
	if !combined {
		errCap = newCapWriter(stderr, maxLoggedOutput)
	}

	start := time.Now()
	err := l.inner.Run(ctx, env, args, outCap, errCap)

	attrs := []any{"op", op, "took", time.Since(start).Round(time.Millisecond), "exit", exitCodeOf(err)}
	level, msg := slog.LevelInfo, "restic finished"
	if err != nil {
		level, msg = slog.LevelWarn, "restic failed"
		if exitCodeOf(err) < 0 {
			attrs = append(attrs, "error", err.Error())
		}
	}
	if combined {
		attrs = append(attrs, "output_bytes", outCap.size())
	} else {
		attrs = append(attrs, "stdout_bytes", outCap.size(), "stderr_bytes", errCap.size())
	}
	if l.mode >= LogResponses {
		switch {
		case combined:
			attrs = appendText(attrs, "output", outCap)
		case op == "dump":
			attrs = append(attrs, "stdout", fileDataNotice)
			attrs = appendText(attrs, "stderr", errCap)
		default:
			attrs = appendText(attrs, "stdout", outCap)
			attrs = appendText(attrs, "stderr", errCap)
		}
	}
	l.log.Log(ctx, level, msg, attrs...)
	return err
}

// appendText adds what a stream held, when it held anything.
func appendText(attrs []any, key string, w *capWriter) []any {
	if w.size() == 0 {
		return attrs
	}
	return append(attrs, key, w.text())
}

// subcommand is the restic command of an argument list ("ls" in "-r repo ls ...").
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-r" || a == "--repo":
			i++ // skip the repository
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// commandLine renders the call as a line that can be pasted into a shell.
func commandLine(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "restic")
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// exitCodeOf is the exit status of a finished command, or -1 when it did not run to an exit.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

// sameWriter reports whether a and b are the same writer, as os/exec decides it.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() { _ = recover() }() // uncomparable dynamic types are not the same
	return a == b
}

// capWriter passes everything on to dst while remembering the first limit bytes.
type capWriter struct {
	dst   io.Writer
	limit int

	mu    sync.Mutex
	buf   bytes.Buffer
	total int64
}

func newCapWriter(dst io.Writer, limit int) *capWriter {
	if dst == nil {
		dst = io.Discard
	}
	return &capWriter{dst: dst, limit: limit}
}

func (w *capWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.limit - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(room, n)])
	}
	w.total += int64(n)
	return n, err
}

func (w *capWriter) size() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total
}

// text is the captured output, marked when it was cut off.
func (w *capWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := strings.ToValidUTF8(w.buf.String(), "�")
	if w.total > int64(w.buf.Len()) {
		s += fmt.Sprintf("… (truncated, %d bytes in total)", w.total)
	}
	return s
}
