//go:build windows && !cgo

package rpa

import (
	"fmt"
	"time"
)

// winPlatformNoCgo is compiled only when CGO_ENABLED=0. The real Windows
// adapter needs cgo (gocv/OpenCV + robotgo); this stub keeps the package
// cross-compilable for type checking and fails with an actionable message
// instead of silently pretending to drive a desktop.
type winPlatformNoCgo struct{}

// NewPlatform returns the stub adapter used in cgo-disabled builds.
func NewPlatform(_ int, _ time.Duration, _ bool) (Platform, error) {
	return winPlatformNoCgo{}, nil
}

var errCgoRequired = fmt.Errorf("native desktop support requires building with CGO_ENABLED=1 and OpenCV installed")

func (winPlatformNoCgo) ListWindows() ([]WindowInfo, error) {
	return nil, errCgoRequired
}

func (winPlatformNoCgo) FocusWindow(string) (WindowInfo, error) {
	return WindowInfo{}, errCgoRequired
}

func (winPlatformNoCgo) CheckWindow(string) error { return errCgoRequired }

func (winPlatformNoCgo) ActivateWindow(uintptr, time.Time) error { return errCgoRequired }

func (winPlatformNoCgo) CaptureScreen(string) error { return errCgoRequired }

func (winPlatformNoCgo) MatchTemplate(string, string) (Match, error) {
	return Match{}, errCgoRequired
}

func (winPlatformNoCgo) MoveMouse(int, int) error { return errCgoRequired }

func (winPlatformNoCgo) MouseClick(int) error { return errCgoRequired }

func (winPlatformNoCgo) SendKey(string) error { return errCgoRequired }
