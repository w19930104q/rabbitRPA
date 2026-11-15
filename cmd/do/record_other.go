//go:build !windows

package main

import "github.com/yasutakatou/rabbitRPA/internal/rpa"

// runRecording refuses record mode off Windows: global keyboard/mouse hooks
// and screenshot capture are Win32-only. Replay remains fully testable via
// the fake adapter.
func runRecording(o *options, cwd, tmpBase string, p rpa.Platform) int {
	_ = o
	_ = cwd
	_ = tmpBase
	_ = p
	println("record mode is only supported on Windows")
	return exitPlatform
}
