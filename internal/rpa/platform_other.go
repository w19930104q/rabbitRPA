//go:build !windows

package rpa

import (
	"errors"
	"time"
)

// ErrNativeUnsupported is returned when a native desktop adapter is requested
// on a platform other than Windows. rabbitRPA only drives Win32 desktops.
var ErrNativeUnsupported = errors.New("native desktop automation is only supported on Windows; set RABBITRPA_FAKE=1 to run against the scripted test adapter")

// NewPlatform on non-Windows builds refuses instead of silently doing
// nothing. Replay transactions can still be exercised deterministically with
// the fake adapter from package internal/rpa/fake (used by the acceptance
// tests, no real desktop involved).
func NewPlatform(_ int, _ time.Duration, _ bool) (Platform, error) {
	return nil, ErrNativeUnsupported
}
