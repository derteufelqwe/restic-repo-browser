package restic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failed restic invocation by its documented exit code.
type Kind int

// The failure classes the application reacts to.
const (
	KindOther         Kind = iota
	KindNotFound           // exit 10: no repository at the path
	KindLocked             // exit 11: repository is locked
	KindWrongPassword      // exit 12: password is wrong
	KindBinaryMissing      // restic executable could not be started
)

// Error is a failed restic command.
type Error struct {
	Kind    Kind
	Code    int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("restic exited with code %d", e.Code)
	}
	return e.Message
}

// Is lets errors.Is(err, restic.ErrWrongPassword) style checks work on kinds.
func (e *Error) Is(target error) bool {
	k, ok := target.(kindError)
	return ok && Kind(k) == e.Kind
}

type kindError Kind

func (kindError) Error() string { return "restic error kind" }

// Sentinels for errors.Is.
var (
	ErrNotFound      error = kindError(KindNotFound)
	ErrLocked        error = kindError(KindLocked)
	ErrWrongPassword error = kindError(KindWrongPassword)
	ErrBinaryMissing error = kindError(KindBinaryMissing)
)

// UserMessage returns a short sentence fit for display next to a repository.
func UserMessage(err error) string {
	var re *Error
	if !errors.As(err, &re) {
		return err.Error()
	}
	switch re.Kind {
	case KindNotFound:
		return "No restic repository found at this path."
	case KindWrongPassword:
		return "The repository password is wrong."
	case KindLocked:
		return "The repository is locked by another process."
	case KindBinaryMissing:
		return "The restic program could not be started."
	}
	return re.Message
}

func classify(code int) Kind {
	switch code {
	case 10:
		return KindNotFound
	case 11:
		return KindLocked
	case 12:
		return KindWrongPassword
	}
	return KindOther
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
