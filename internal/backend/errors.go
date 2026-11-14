package backend

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a failed backend call.
type ErrorKind int

// The failure classes the application reacts to.
const (
	KindOther         ErrorKind = iota
	KindNotFound                // no repository at the location
	KindLocked                  // the repository is locked
	KindWrongPassword           // the password is wrong
	KindBinaryMissing           // the tool behind the backend could not be started
	KindUnsupported             // the backend does not offer the operation
)

// Error is a failed backend call.
type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "backend call failed"
	}
	return e.Message
}

// Is lets errors.Is(err, backend.ErrWrongPassword) style checks work on kinds.
func (e *Error) Is(target error) bool {
	k, ok := target.(kindError)
	return ok && ErrorKind(k) == e.Kind
}

type kindError ErrorKind

func (kindError) Error() string { return "backend error kind" }

// Sentinels for errors.Is.
var (
	ErrNotFound      error = kindError(KindNotFound)
	ErrLocked        error = kindError(KindLocked)
	ErrWrongPassword error = kindError(KindWrongPassword)
	ErrBinaryMissing error = kindError(KindBinaryMissing)
	ErrUnsupported   error = kindError(KindUnsupported)
)

// Unsupported is the error for an operation or repository kind a backend lacks.
func Unsupported(format string, args ...any) error {
	return &Error{Kind: KindUnsupported, Message: fmt.Sprintf(format, args...)}
}

// UserMessage returns a short sentence fit for display next to a repository.
func UserMessage(err error) string {
	var be *Error
	if !errors.As(err, &be) {
		return err.Error()
	}
	switch be.Kind {
	case KindNotFound:
		return "No restic repository found at this location."
	case KindWrongPassword:
		return "The repository password is wrong."
	case KindLocked:
		return "The repository is locked by another process."
	case KindBinaryMissing:
		return "The restic program could not be started."
	}
	return be.Error()
}
