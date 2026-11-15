//go:build windows && cgo

package rpa

import (
	"fmt"
	"image"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-vgo/robotgo"
	"github.com/kbinani/screenshot"
	"github.com/yasutakatou/string2keyboard"
	"gocv.io/x/gocv"
)

// winPlatform implements Platform with the original Win32/OpenCV semantics.
type winPlatform struct {
	try     int
	wait    time.Duration
	debug   bool
	rawChar string
}

// newNativePlatform implements Platform with the original Win32/OpenCV semantics.
func newNativePlatform(try int, wait time.Duration, debug bool) Platform {
	return &winPlatform{try: try, wait: wait, debug: debug, rawChar: "~"}
}

func (p *winPlatform) ListWindows() ([]WindowInfo, error) {
	titles := listWindow(p.debug)
	out := make([]WindowInfo, 0, len(titles))
	for _, t := range titles {
		parts := strings.SplitN(t, " : ", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, WindowInfo{Title: parts[0]})
	}
	return out, nil
}

// FocusWindow reproduces the original bidirectional substring matching but
// returns ErrWindowAmbiguous instead of silently choosing one window.
func (p *winPlatform) FocusWindow(title string) (WindowInfo, error) {
	var rect rectData
	var hits []WindowInfo

	cb := syscall.NewCallback(func(h syscall.Handle, _ uintptr) uintptr {
		b := make([]uint16, 200)
		if _, err := getWindowText(h, &b[0], int32(len(b))); err != nil {
			return 1
		}
		name := syscall.UTF16ToString(b)
		if p.debug {
			fmt.Printf("EnumWindows Search '%s' window: handle=0x%x\n", name, h)
		}
		if matchCheck(title, name) {
			getWindowRect(hwnd(h), &rect, p.debug)
			if int(rect.Right-rect.Left) > 0 && int(rect.Bottom-rect.Top) > 0 {
				hits = append(hits, WindowInfo{
					Handle: uintptr(h),
					Title:  name,
					Rect: image.Rect(
						int(rect.Left), int(rect.Top),
						int(rect.Right), int(rect.Bottom)),
				})
			}
		}
		return 1
	})
	enumWindows(cb, 0)

	switch len(hits) {
	case 0:
		return WindowInfo{}, fmt.Errorf("%w: %q", ErrWindowMissing, title)
	case 1:
		return hits[0], nil
	default:
		return WindowInfo{}, fmt.Errorf("%w: %q matches %d windows", ErrWindowAmbiguous, title, len(hits))
	}
}

func (p *winPlatform) CheckWindow(title string) error {
	if title == "" {
		return nil
	}
	_, err := p.FocusWindow(title)
	return err
}

// ActivateWindow keeps the original SetActiveWindow/SetForegroundWindow retry
// loop, bounded by deadline.
func (p *winPlatform) ActivateWindow(hwnd uintptr, deadline time.Time) error {
	for {
		if hwnd != getForegroundWindow(p.debug) {
			setActiveWindow(hwndType(hwnd), p.debug)
		} else {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: handle 0x%x could not be foregrounded", ErrWindowMissing, hwnd)
		}
		time.Sleep(p.wait)
	}
}

func (p *winPlatform) CaptureScreen(path string) error {
	n := screenshot.NumActiveDisplays()
	if n <= 0 {
		return fmt.Errorf("active display not found")
	}
	all := image.Rect(0, 0, 0, 0)
	for i := 0; i < n; i++ {
		all = all.Union(screenshot.GetDisplayBounds(i))
	}
	bitmap := robotgo.CaptureScreen(all.Min.X, all.Min.Y, all.Dx(), all.Dy())
	if bitmap == nil {
		return fmt.Errorf("CaptureScreen returned no bitmap")
	}
	defer robotgo.FreeBitmap(bitmap)
	if saved := robotgo.SaveBitmap(bitmap, path); saved != path {
		return fmt.Errorf("SaveBitmap failed: %s", path)
	}
	return nil
}

// MatchTemplate is the original gocv grayscale TM_CCOEFF_NORMED flow with
// deterministic Mat lifetime management.
func (p *winPlatform) MatchTemplate(screenPath, templatePath string) (Match, error) {
	matImage := gocv.IMRead(screenPath, gocv.IMReadGrayScale)
	defer matImage.Close()
	if matImage.Empty() {
		return Match{}, fmt.Errorf("cannot read screenshot: %s", screenPath)
	}
	matTemplate := gocv.IMRead(templatePath, gocv.IMReadGrayScale)
	defer matTemplate.Close()
	if matTemplate.Empty() {
		return Match{}, fmt.Errorf("cannot read template: %s", templatePath)
	}
	if matTemplate.Cols() > matImage.Cols() || matTemplate.Rows() > matImage.Rows() {
		return Match{}, fmt.Errorf("template %s is larger than screenshot %s", templatePath, screenPath)
	}

	matResult := gocv.NewMat()
	defer matResult.Close()
	mask := gocv.NewMat()
	defer mask.Close()

	gocv.MatchTemplate(matImage, matTemplate, &matResult, gocv.TmCcoeffNormed, mask)
	_, maxConfidence, _, maxLoc := gocv.MinMaxLoc(matResult)
	if p.debug {
		fmt.Println("mouseLocate maxConfidence:", maxConfidence, "at", maxLoc)
	}
	return Match{Confidence: maxConfidence, Point: maxLoc}, nil
}

func (p *winPlatform) MoveMouse(x, y int) error {
	robotgo.MoveMouseSmooth(x, y, 0.01, 0.01)
	return nil
}

// MouseClick: original semantics where button == 1 means left click and any
// other recorded value is a right click.
func (p *winPlatform) MouseClick(button int) error {
	if button == 1 {
		robotgo.MouseClick("left", false)
	} else {
		robotgo.MouseClick("right", false)
	}
	return nil
}

// SendKey keeps the original "ctrl+"/"alt+" prefix and "~<rawcode>~" escape
// handling of string2keyboard.KeyboardWrite.
func (p *winPlatform) SendKey(doCmd string) error {
	cCtrl := false
	cAlt := false
	if strings.Contains(doCmd, "ctrl+") {
		cCtrl = true
		doCmd = strings.Replace(doCmd, "ctrl+", "", 1)
	}
	if strings.Contains(doCmd, "alt+") {
		cAlt = true
		doCmd = strings.Replace(doCmd, "alt+", "", 1)
	}
	string2keyboard.KeyboardWrite(doCmd, cCtrl, cAlt, p.rawChar)
	if p.debug {
		fmt.Println("KeyInput:", doCmd, cCtrl, cAlt)
	}
	return nil
}

// --- Win32 plumbing (ported verbatim from the original do.go) -------------

type (
	handle   uintptr
	hwndType handle
)

type rectData struct {
	Left, Top, Right, Bottom int32
}

var (
	user32                  = syscall.MustLoadDLL("user32.dll")
	procEnumWindows         = user32.MustFindProc("EnumWindows")
	procGetWindowTextW      = user32.MustFindProc("GetWindowTextW")
	procSetActiveWindow     = user32.MustFindProc("SetActiveWindow")
	procSetForegroundWindow = user32.MustFindProc("SetForegroundWindow")
	procGetForegroundWindow = user32.MustFindProc("GetForegroundWindow")
	procGetWindowRect       = user32.MustFindProc("GetWindowRect")
)

func matchCheck(a, b string) bool {
	return strings.Contains(b, a) || strings.Contains(a, b)
}

func listWindow(debug bool) []string {
	var rect rectData
	ret := []string{}
	cb := syscall.NewCallback(func(h syscall.Handle, _ uintptr) uintptr {
		b := make([]uint16, 200)
		if _, err := getWindowText(h, &b[0], int32(len(b))); err != nil {
			return 1
		}
		getWindowRect(hwndType(h), &rect, debug)
		if rect.Left != 0 || rect.Top != 0 || rect.Right != 0 || rect.Bottom != 0 {
			ret = append(ret, fmt.Sprintf("%s : %x", syscall.UTF16ToString(b), h))
		}
		return 1
	})
	enumWindows(cb, 0)
	return ret
}

func getWindowText(hwnd syscall.Handle, str *uint16, maxCount int32) (int32, error) {
	r0, _, e1 := syscall.Syscall(procGetWindowTextW.Addr(), 3,
		uintptr(hwnd), uintptr(unsafe.Pointer(str)), uintptr(maxCount))
	n := int32(r0)
	if n == 0 {
		if e1 != 0 {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return n, nil
}

func getWindowRect(hwnd hwndType, rect *rectData, debug bool) error {
	r1, _, e1 := syscall.Syscall(procGetWindowRect.Addr(), 7,
		uintptr(hwnd), uintptr(unsafe.Pointer(rect)), 0)
	if r1 == 0 {
		if e1 != 0 {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func enumWindows(enumFunc, lparam uintptr) error {
	r1, _, e1 := syscall.Syscall(procEnumWindows.Addr(), 2, enumFunc, lparam, 0)
	if r1 == 0 {
		if e1 != 0 {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func getForegroundWindow(debug bool) uintptr {
	hwnd, _, _ := syscall.Syscall(procGetForegroundWindow.Addr(), 6, 0, 0, 0)
	if debug {
		fmt.Printf("currentWindow: handle=0x%x.\n", hwnd)
	}
	return hwnd
}

func setActiveWindow(hwnd hwndType, debug bool) {
	if debug {
		fmt.Printf("SetActiveWindow: handle=0x%x.\n", hwnd)
	}
	syscall.Syscall(procSetActiveWindow.Addr(), 4, uintptr(hwnd), 0, 0)
	syscall.Syscall(procSetForegroundWindow.Addr(), 5, uintptr(hwnd), 0, 0)
}
