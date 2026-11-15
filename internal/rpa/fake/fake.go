// Package fake provides a scripted Platform implementation for replay
// acceptance tests. It performs no real desktop interaction: windows are an
// in-memory list, screenshots are deterministic 1x1 PNG/BMP files and
// template matches come from a script queue or an inspectable default.
package fake

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yasutakatou/rabbitRPA/internal/rpa"
)

// Call records one platform invocation so tests can assert the exact sequence
// and arguments of a replay.
type Call struct {
	Op     string // "focus", "activate", "capture", "match", "move", "click", "key", "list", "check"
	Arg    string
	Arg2   string
	X, Y   int
	Button int
	At     time.Time
}

// MatchScript tells the fake how MatchTemplate should answer on the nth call.
type MatchScript struct {
	Match rpa.Match
	Err   error
}

// WindowScript is the fake's window universe.
type WindowScript struct {
	Windows []rpa.WindowInfo
	// FocusErr forces FocusWindow to return this error for any requested title.
	FocusErr error
	// ActivateFail handles make ActivateWindow fail for this handle.
	ActivateFail map[uintptr]error
}

// Platform is the scripted fake desktop.
type Platform struct {
	mu sync.Mutex

	windows       []rpa.WindowInfo
	focusErr      error
	activateFail  map[uintptr]error
	matchQueue    []MatchScript
	matchCalls    int
	captureFailAt int // 1-based call number that fails; 0 = never
	captureCalls  int

	// CaptureContent, when set, is written for every screenshot (raw bytes).
	CaptureContent []byte

	// DefaultMatch answers MatchTemplate when the script queue is exhausted.
	DefaultMatch rpa.Match

	// Log is the call trace.
	Log []Call

	// Keys/Moves/Clicks are convenience slices.
	Keys   []string
	Moves  []image.Point
	Clicks []Click
}

// Click records one MouseClick.
type Click struct {
	Point  image.Point
	Button int
}

// New builds a fake platform.
func New(w WindowScript, matches []MatchScript) *Platform {
	return &Platform{
		windows:       append([]rpa.WindowInfo(nil), w.Windows...),
		focusErr:      w.FocusErr,
		activateFail:  copyErrMap(w.ActivateFail),
		matchQueue:    append([]MatchScript(nil), matches...),
		DefaultMatch:  rpa.Match{Confidence: 1.0, Point: image.Pt(0, 0)},
		captureFailAt: 0,
	}
}

func copyErrMap(in map[uintptr]error) map[uintptr]error {
	if len(in) == 0 {
		return map[uintptr]error{}
	}
	out := make(map[uintptr]error, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SetCaptureFail makes the nth (1-based) CaptureScreen call fail.
func (p *Platform) SetCaptureFail(nth int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captureFailAt = nth
}

// Calls returns a copy of the invocation trace.
func (p *Platform) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Call, len(p.Log))
	copy(out, p.Log)
	return out
}

func (p *Platform) record(c Call) {
	c.At = time.Now().UTC()
	p.Log = append(p.Log, c)
}

// ListWindows implements rpa.Platform.
func (p *Platform) ListWindows() ([]rpa.WindowInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "list"})
	out := make([]rpa.WindowInfo, len(p.windows))
	copy(out, p.windows)
	return out, nil
}

// matchTitle reproduces the original bidirectional substring rule.
func matchTitle(want, got string) bool {
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// FocusWindow implements rpa.Platform with explicit ambiguity detection.
func (p *Platform) FocusWindow(title string) (rpa.WindowInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "focus", Arg: title})
	if p.focusErr != nil {
		return rpa.WindowInfo{}, p.focusErr
	}
	var hits []rpa.WindowInfo
	for _, w := range p.windows {
		if matchTitle(title, w.Title) {
			if w.Rect.Dx() > 0 && w.Rect.Dy() > 0 {
				hits = append(hits, w)
			}
		}
	}
	switch len(hits) {
	case 0:
		return rpa.WindowInfo{}, fmt.Errorf("%w: %q", rpa.ErrWindowMissing, title)
	case 1:
		return hits[0], nil
	default:
		return rpa.WindowInfo{}, fmt.Errorf("%w: %q matches %d windows", rpa.ErrWindowAmbiguous, title, len(hits))
	}
}

// CheckWindow implements rpa.WindowPrechecker. It reuses the exact
// FocusWindow matching rules (including ambiguity detection); FocusWindow
// already records the invocation trace.
func (p *Platform) CheckWindow(title string) error {
	if title == "" {
		return nil
	}
	_, err := p.FocusWindow(title)
	return err
}

// ActivateWindow implements rpa.Platform. The fake never retries; deadline is
// recorded for assertions.
func (p *Platform) ActivateWindow(hwnd uintptr, deadline time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "activate", Arg: fmt.Sprintf("0x%x", hwnd)})
	if err, ok := p.activateFail[hwnd]; ok {
		return err
	}
	for _, w := range p.windows {
		if w.Handle == hwnd {
			return nil
		}
	}
	return fmt.Errorf("%w: handle 0x%x", rpa.ErrWindowMissing, hwnd)
}

// CaptureScreen writes a deterministic 1x1 image to path.
func (p *Platform) CaptureScreen(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captureCalls++
	p.record(Call{Op: "capture", Arg: path})
	if p.captureFailAt != 0 && p.captureCalls == p.captureFailAt {
		return errors.New("forced capture failure")
	}
	if len(p.CaptureContent) > 0 {
		return os.WriteFile(path, p.CaptureContent, 0o600)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// MatchTemplate verifies both files exist and answers from the script queue.
func (p *Platform) MatchTemplate(screenPath, templatePath string) (rpa.Match, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "match", Arg: screenPath, Arg2: templatePath})
	for _, path := range []string{screenPath, templatePath} {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return rpa.Match{}, fmt.Errorf("image missing: %s: %w", path, err)
			}
			return rpa.Match{}, err
		}
		if info.IsDir() || info.Size() == 0 {
			return rpa.Match{}, fmt.Errorf("image invalid: %s", path)
		}
	}
	idx := p.matchCalls
	p.matchCalls++
	if idx < len(p.matchQueue) {
		return p.matchQueue[idx].Match, p.matchQueue[idx].Err
	}
	return p.DefaultMatch, nil
}

// MoveMouse implements rpa.Platform.
func (p *Platform) MoveMouse(x, y int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "move", X: x, Y: y})
	p.Moves = append(p.Moves, image.Pt(x, y))
	return nil
}

// MouseClick implements rpa.Platform.
func (p *Platform) MouseClick(button int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "click", Button: button})
	last := image.Point{}
	if n := len(p.Moves); n > 0 {
		last = p.Moves[n-1]
	}
	p.Clicks = append(p.Clicks, Click{Point: last, Button: button})
	return nil
}

// SendKey implements rpa.Platform.
func (p *Platform) SendKey(params string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(Call{Op: "key", Arg: params})
	p.Keys = append(p.Keys, params)
	return nil
}

// MatchCallCount returns how many MatchTemplate calls happened.
func (p *Platform) MatchCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.matchCalls
}
