// Package assets holds the bundled do.exe/OpenCV payloads.
//
// The upstream project generated a statik bundle at release time, which is
// not committed to the repository; go:embed gives the same single-binary
// extraction behaviour with a committed, buildable source tree. Drop the
// real do.exe and libopencv_*.dll files into dist/ (or keep the placeholder)
// and they are embedded automatically.
package assets

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed dist/*
var dist embed.FS

// Names are the payloads the launcher extracts before starting do.exe.
var Names = []string{
	"do.exe",
	"libopencv_calib3d430.dll",
	"libopencv_core430.dll",
	"libopencv_dnn430.dll",
	"libopencv_features2d430.dll",
	"libopencv_flann430.dll",
	"libopencv_highgui430.dll",
	"libopencv_imgcodecs430.dll",
	"libopencv_imgproc430.dll",
	"libopencv_objdetect430.dll",
	"libopencv_video430.dll",
	"libopencv_videoio430.dll",
}

// ErrPlaceholder is returned when only the placeholder file was embedded,
// i.e. the real payloads were never placed in dist/.
var ErrPlaceholder = errors.New("embedded payload is a placeholder; build the real do.exe and place payloads in internal/assets/dist")

// Open returns the embedded payload content.
func Open(name string) (fs.File, error) {
	return dist.Open("dist/" + name)
}

// IsPlaceholder reports whether name is the committed 0-byte placeholder
// rather than a real payload.
func IsPlaceholder(name string) bool {
	f, err := dist.Open("dist/" + name)
	if err != nil {
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return true
	}
	return info.Size() == 0
}
