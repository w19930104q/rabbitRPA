//go:build windows && !cgo

package main

import "github.com/yasutakatou/rabbitRPA/internal/rpa"

// runRecording needs cgo (global hooks + OpenCV); fail explicitly instead of
// silently doing nothing.
func runRecording(o *options, cwd, tmpBase string, p rpa.Platform) int {
	_ = o
	_ = cwd
	_ = tmpBase
	_ = p
	println("record mode requires building with CGO_ENABLED=1 and OpenCV installed")
	return exitPlatform
}
