//go:build windows && cgo

package main

import (
	"image"

	"gocv.io/x/gocv"
)

// gocvIMReadGray reads an image file as grayscale. Callers must Close the Mat.
func gocvIMReadGray(path string) gocv.Mat {
	return gocv.IMRead(path, gocv.IMReadGrayScale)
}

// gocvMatch runs the original TM_CCOEFF_NORMED template match and returns
// the best location/score with every Mat released.
func gocvMatch(src, templ gocv.Mat) (minConfidence, maxConfidence float32, minLoc, maxLoc image.Point) {
	result := gocv.NewMat()
	defer result.Close()
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.MatchTemplate(src, templ, &result, gocv.TmCcoeffNormed, mask)
	return gocv.MinMaxLoc(result)
}
