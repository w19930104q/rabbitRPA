//go:build windows && cgo

package rpa

import "time"

// NewPlatform returns the native Win32/OpenCV adapter.
func NewPlatform(try int, wait time.Duration, debug bool) (Platform, error) {
	return newNativePlatform(try, wait, debug), nil
}
