package main

import "image"

// rectOf gives fake adapter windows non-zero, non-overlapping bounds so the
// original "skip zero-sized windows" rule is exercised.
func rectOf(i int) image.Rectangle {
	return image.Rect(0, i*100, 640, (i+1)*100)
}
