package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"

	"restic-repo-browser/internal/backend"
)

// kindOf classifies a failure by restic's documented exit code.
func kindOf(code int) backend.ErrorKind {
	switch code {
	case 10:
		return backend.KindNotFound
	case 11:
		return backend.KindLocked
	case 12:
		return backend.KindWrongPassword
	}
	return backend.KindOther
}

// wrapRunError turns the error of a finished (or never started) command into a
// *backend.Error. stdout and stderr hold what the command printed.
func wrapRunError(ctx context.Context, err error, stdout, stderr []byte) error {
	if ctx.Err() != nil {
		return ctx.Err() // cancelled or timed out: not a restic failure
	}
	var coded interface{ ExitCode() int } // *exec.ExitError and test fakes
	if errors.As(err, &coded) {
		code := coded.ExitCode()
		msg := messageFrom(stdout, stderr)
		if msg == "" {
			msg = fmt.Sprintf("restic exited with code %d", code)
		}
		return &backend.Error{Kind: kindOf(code), Message: msg}
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) || errors.Is(err, fs.ErrNotExist) {
		return &backend.Error{Kind: backend.KindBinaryMissing, Message: err.Error()}
	}
	return err
}

// messageFrom extracts the most useful error text. restic prints a JSON
// "exit_error" object in --json mode and plain text on stderr otherwise.
func messageFrom(stdout, stderr []byte) string {
	for _, buf := range [][]byte{stdout, stderr} {
		for _, line := range bytes.Split(bytes.TrimSpace(buf), []byte("\n")) {
			var m struct {
				Type    string `json:"message_type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(line, &m) == nil && m.Type == "exit_error" && m.Message != "" {
				return strings.TrimSpace(m.Message)
			}
		}
	}
	return strings.TrimSpace(string(stderr))
}
