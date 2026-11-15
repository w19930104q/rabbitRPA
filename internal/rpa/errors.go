package rpa

import (
	"errors"
	"strconv"
)

// Sentinel errors. Callers (including tests) can match these with errors.Is.
var (
	// ErrWindowMissing means no top-level window matched the requested title.
	ErrWindowMissing = errors.New("target window not found")
	// ErrWindowAmbiguous means several non-zero-sized windows matched the
	// requested title, so focusing one silently would be nondeterministic.
	ErrWindowAmbiguous = errors.New("target window title is ambiguous")
	// ErrInterrupted means the replay was cancelled before every step ran.
	ErrInterrupted = errors.New("interrupted")
	// ErrPreflight means the strict input checks failed before any step ran.
	ErrPreflight = errors.New("preflight failed")
)

// LineError annotates a TSV parsing failure with the offending line number.
type LineError struct {
	Line int
	Msg  string
}

func (e *LineError) Error() string {
	return "line " + strconv.Itoa(e.Line) + ": " + e.Msg
}
