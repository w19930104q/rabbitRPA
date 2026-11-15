//go:build windows && cgo

package main

import (
	"crypto/md5"
	"fmt"
	"image"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/go-vgo/robotgo"
	"github.com/kbinani/screenshot"
	hook "github.com/robotn/gohook"
	"github.com/yasutakatou/rabbitRPA/internal/rpa"
)

type (
	winHandle uintptr
	winHWND   winHandle
)

type winRECT struct {
	Left, Top, Right, Bottom int32
}

// recorder owns all record-mode state. No package-level mutable state means
// two invocations in the same process cannot leak History or temp names.
type recorder struct {
	cfg          options
	cwd          string
	tmpBase      string
	history      []rpa.HistoryItem
	hashes       []hashData
	letters      []rune
	preWindow    string
	aftWindow    string
	tempX, tempY int
	rawChar      string

	user32                  *syscall.DLL
	procEnumWindows         *syscall.Proc
	procGetWindowTextW      *syscall.Proc
	procSetActiveWindow     *syscall.Proc
	procSetForegroundWindow *syscall.Proc
	procGetForegroundWindow *syscall.Proc
	procGetWindowRect       *syscall.Proc
}

type hashData struct {
	Filename string
	Hash     string
}

func newRecorder(o options, cwd, tmpBase string) (*recorder, error) {
	if err := os.MkdirAll(tmpBase, 0o777); err != nil {
		return nil, err
	}
	dll := syscall.MustLoadDLL("user32.dll")
	return &recorder{
		cfg:                     o,
		cwd:                     cwd,
		tmpBase:                 tmpBase,
		letters:                 []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		rawChar:                 "~",
		user32:                  dll,
		procEnumWindows:         dll.MustFindProc("EnumWindows"),
		procGetWindowTextW:      dll.MustFindProc("GetWindowTextW"),
		procSetActiveWindow:     dll.MustFindProc("SetActiveWindow"),
		procSetForegroundWindow: dll.MustFindProc("SetForegroundWindow"),
		procGetForegroundWindow: dll.MustFindProc("GetForegroundWindow"),
		procGetWindowRect:       dll.MustFindProc("GetWindowRect"),
	}, nil
}

func runRecording(o *options, cwd, tmpBase string, _ rpa.Platform) int {
	// Original semantics: record mode owns and clears the tmp directory
	// first; replay never removes it.
	if err := os.RemoveAll(tmpBase); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitReplayFailure
	}
	rec, err := newRecorder(o, cwd, tmpBase)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitReplayFailure
	}
	if err := rec.loop(filepath.Join(cwd, o.export)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitReplayFailure
	}
	return exitOK
}

func (r *recorder) loop(exportFile string) error {
	fmt.Printf(" - - recording start! you want to end this mode, key press ascii code (%d) - - \n", r.cfg.exitCode)

	altFlag := 0
	actFlag := false
	var bufStrs uint16

	// Captures are written via absolute paths into the tmp directory; the
	// process working directory is never changed.
	evChan := hook.Start()
	defer hook.End()

	for ev := range evChan {
		strs := ""
		if actFlag {
			if ev.Kind == 3 { // KeyDown
				bufStrs, strs = r.keyDown(altFlag, int(ev.Rawcode), strs, string(ev.Keychar), bufStrs)
				r.preWindow = r.aftWindow
				r.aftWindow = r.getHwndToTitle(r.getForegroundWindow(false))
			}
			if ev.Kind == 4 || ev.Kind == 5 { // KeyHold, KeyUp
				altFlag = r.keyHoldUp(int(ev.Rawcode), int(ev.Kind), bufStrs, exportFile)
				if altFlag == 256 {
					return nil
				}
				r.preWindow = r.aftWindow
				r.aftWindow = r.getHwndToTitle(r.getForegroundWindow(false))
			}
			if ev.Kind == 9 { // MouseMove
				if r.moveValCheck(int(ev.X), int(ev.Y)) {
					r.addMouseMove(int(ev.X), int(ev.Y))
				}
				r.preWindow = r.aftWindow
				r.aftWindow = r.getHwndToTitle(r.getForegroundWindow(false))
			}
		}
		if ev.Kind == 7 { // MouseHold
			if !actFlag {
				actFlag = true
				r.aftWindow = r.getHwndToTitle(r.getForegroundWindow(false))
			}
			r.addMouseAction(int(ev.Button), int(ev.X), int(ev.Y))
			r.preWindow = r.aftWindow
			r.aftWindow = r.getHwndToTitle(r.getForegroundWindow(false))
		}
	}
	return nil
}

func (r *recorder) moveValCheck(evX, evY int) bool {
	threshold := int(r.cfg.move)
	if r.tempX-evX > threshold || evX-r.tempX > threshold {
		return true
	}
	if r.tempY-evY > threshold/2 || evY-r.tempY > threshold/2 {
		return true
	}
	return false
}

func (r *recorder) keyDown(altFlag, rawcode int, strs, keyChar string, bufStrs uint16) (uint16, string) {
	if altFlag == 0 {
		switch rawcode {
		case 8:
			strs = `\b`
		case 9:
			strs = `\t`
		case 13:
			strs = `\n`
		default:
			strs = keyChar
		}
	} else {
		switch altFlag {
		case 162:
			strs = "ctrl+" + keyChar
		case 164:
			strs = "alt+" + keyChar
		}
	}
	bufStrs = uint16(rawcode)
	r.addHistory(rpa.DeviceKey, strs)
	return bufStrs, strs
}

func (r *recorder) keyHoldUp(rawcode, kind int, bufStrs uint16, exportFile string) int {
	altFlag := 0
	switch rawcode {
	case 162, 164: // Ctrl, Alt
		if kind == 4 {
			altFlag = rawcode
		}
	case r.cfg.exitCode:
		if err := r.exportHistory(exportFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return 256
	case 160:
	default:
		if kind == 5 && int(bufStrs) != rawcode {
			r.addHistory(rpa.DeviceKey, `\`+r.rawChar+strconv.Itoa(rawcode)+r.rawChar)
		}
	}
	return altFlag
}

func (r *recorder) addMouseMove(evX, evY int) {
	var rect winRECT
	moveName := r.randStr(8) + ".bmp"
	current := r.getForegroundWindow(false)
	r.getWindowRect(winHWND(current), &rect, false)
	moveName, moveHash := r.captureCase(filepath.Join(r.tmpBase, moveName), current, true)
	if moveName == "" {
		moveName, _ = r.searchHash(moveHash, true)
	}
	if evX > 0 && evX > int(rect.Left) && evX < int(rect.Right) &&
		evY > 0 && evX > int(rect.Top) && evY < int(rect.Bottom) {
		targetX := evX - int(rect.Left)
		targetY := evY - int(rect.Top)
		r.addHistory(rpa.DeviceMove,
			moveName+";"+moveHash+";"+strconv.Itoa(targetX)+";"+strconv.Itoa(targetY))
		r.tempX, r.tempY = evX, evY
	}
}

func (r *recorder) addMouseAction(evButton, evX, evY int) {
	allName := r.randStr(8) + ".bmp"
	allName, allHash := r.captureCase(filepath.Join(r.tmpBase, allName), 0, true)
	if allName == "" {
		allName, _ = r.searchHash(allHash, true)
	}
	partName := r.randStr(8) + ".bmp"
	current := r.getForegroundWindow(r.cfg.debug)
	partName, partHash := r.captureCase(filepath.Join(r.tmpBase, partName), current, false)
	if partName == "" {
		partName, _ = r.searchHash(partHash, false)
	}
	if _, err := os.Stat(filepath.Join(r.tmpBase, allName)); err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(r.tmpBase, partName)); err != nil {
		return
	}
	// MatchTemplate is used at record time to compute click offsets, exactly
	// like the original implementation.
	matImage := gocvIMReadGray(filepath.Join(r.tmpBase, allName))
	defer matImage.Close()
	matTemplate := gocvIMReadGray(filepath.Join(r.tmpBase, partName))
	defer matTemplate.Close()
	_, _, _, maxLoc := gocvMatch(matImage, matTemplate)

	targetX := evX - maxLoc.X
	targetY := evY - maxLoc.Y
	r.tempX, r.tempY = evX, evY
	r.addHistory(rpa.DeviceClick,
		allName+";"+allHash+";"+partName+";"+partHash+";"+
			strconv.Itoa(targetX)+";"+strconv.Itoa(targetY)+";"+strconv.Itoa(evButton))
}

func (r *recorder) addHistory(device, params string) {
	if params == "" {
		return
	}
	r.history = append(r.history, rpa.HistoryItem{
		Device: device, Pre: r.preWindow, Aft: r.aftWindow, Params: params,
		Line: len(r.history) + 1,
	})
	if r.cfg.debug {
		fmt.Println("liveRecord: ", params)
	}
}

func (r *recorder) exportHistory(filename string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	return rpa.ExportHistory(f, r.history)
}

func (r *recorder) captureCase(absPath string, hwnd uintptr, allPart bool) (string, string) {
	var rect winRECT
	if hwnd == 0 {
		n := screenshot.NumActiveDisplays()
		if n <= 0 {
			panic("Active display not found")
		}
		all := image.Rect(0, 0, 0, 0)
		for i := 0; i < n; i++ {
			all = all.Union(screenshot.GetDisplayBounds(i))
		}
		bitmap := robotgo.CaptureScreen(all.Min.X, all.Min.Y, all.Dx(), all.Dy())
		robotgo.SaveBitmap(bitmap, absPath)
		robotgo.FreeBitmap(bitmap)
	} else {
		r.getWindowRect(winHWND(hwnd), &rect, r.cfg.debug)
		bitmap := robotgo.CaptureScreen(int(rect.Left), int(rect.Top),
			int(rect.Right-rect.Left), int(rect.Bottom-rect.Top))
		robotgo.SaveBitmap(bitmap, absPath)
		robotgo.FreeBitmap(bitmap)
	}
	hash := r.calcHash(absPath)
	if _, existing := r.searchHash(hash, allPart); existing == "" {
		return filepath.Base(absPath), hash
	}
	if err := os.Remove(absPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return "", hash
}

func (r *recorder) searchHash(targetHash string, allPart bool) (string, string) {
	for _, it := range r.history {
		if it.Device != rpa.DeviceClick && it.Device != rpa.DeviceMove {
			continue
		}
		s := strings.Split(it.Params, ";")
		if allPart && targetHash == s[1] {
			return s[0], s[1]
		}
		if !allPart && targetHash == s[3] {
			return s[2], s[3]
		}
	}
	return "", ""
}

func (r *recorder) calcHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (r *recorder) getHwndToTitle(target uintptr) string {
	for _, line := range r.listWindow(false) {
		parts := strings.Split(line, " : ")
		if len(parts) == 2 && strings.Contains(fmt.Sprintf("%x", target), parts[1]) {
			return parts[0]
		}
	}
	return ""
}

func (r *recorder) listWindow(debug bool) []string {
	var rect winRECT
	ret := []string{}
	cb := syscall.NewCallback(func(h syscall.Handle, _ uintptr) uintptr {
		b := make([]uint16, 200)
		if _, err := r.getWindowText(winHandle(h), &b[0], int32(len(b))); err != nil {
			return 1
		}
		r.getWindowRect(winHWND(h), &rect, debug)
		if rect.Left != 0 || rect.Top != 0 || rect.Right != 0 || rect.Bottom != 0 {
			ret = append(ret, fmt.Sprintf("%s : %x", syscall.UTF16ToString(b), h))
		}
		return 1
	})
	r.enumWindows(cb, 0)
	return ret
}

func (r *recorder) getWindowText(h winHandle, str *uint16, maxCount int32) (int32, error) {
	r0, _, e1 := syscall.Syscall(r.procGetWindowTextW.Addr(), 3,
		uintptr(h), uintptr(unsafe.Pointer(str)), uintptr(maxCount))
	n := int32(r0)
	if n == 0 {
		if e1 != 0 {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return n, nil
}

func (r *recorder) getWindowRect(h winHWND, rect *winRECT, debug bool) error {
	r1, _, e1 := syscall.Syscall(r.procGetWindowRect.Addr(), 7,
		uintptr(h), uintptr(unsafe.Pointer(rect)), 0)
	if r1 == 0 {
		if e1 != 0 {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func (r *recorder) enumWindows(enumFunc, lparam uintptr) error {
	r1, _, e1 := syscall.Syscall(r.procEnumWindows.Addr(), 2, enumFunc, lparam, 0)
	if r1 == 0 {
		if e1 != 0 {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func (r *recorder) getForegroundWindow(debug bool) uintptr {
	hwnd, _, _ := syscall.Syscall(r.procGetForegroundWindow.Addr(), 6, 0, 0, 0)
	if debug {
		fmt.Printf("currentWindow: handle=0x%x.\n", hwnd)
	}
	return hwnd
}

func (r *recorder) randStr(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = r.letters[rand.Intn(len(r.letters))]
	}
	return string(b)
}
