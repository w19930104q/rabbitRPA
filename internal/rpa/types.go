// Package rpa implements the transactional replay engine and the shared
// recording/replay history model for rabbitRPA.
//
// The TSV recording format and the Windows GUI operation semantics are kept
// identical to the original tool; all platform specific behaviour is hidden
// behind the Platform interface so replay can be driven deterministically
// without a real desktop (see internal/rpa/fake).
package rpa

import (
	"image"
	"time"
)

// HistoryItem is one recorded line of a TSV file.
//
// Recorded format (one line, tab separated):
//
//	Device Pre Aft Params
//
// Device is "key", "click" or "move". Pre/Aft are the foreground window
// titles before/after the action and may be empty.
type HistoryItem struct {
	Device string
	Pre    string
	Aft    string
	Params string
	// Line is the 1-based line number of the item inside the source TSV.
	Line int
}

// Match is the result of locating a template image on a screenshot.
type Match struct {
	// Confidence is the normalised correlation score in [0,1].
	Confidence float32
	// Point is the top-left location of the best match, screen coordinates.
	Point image.Point
}

// WindowInfo describes one top-level window.
type WindowInfo struct {
	Handle uintptr
	Title  string
	Rect   image.Rectangle
}

// Platform abstracts every desktop interaction the replay engine performs.
//
// Implementations must be safe for sequential use by a single Engine; the
// engine never calls methods concurrently.
type Platform interface {
	// ListWindows returns every visible, non-zero sized top-level window.
	ListWindows() ([]WindowInfo, error)
	// FocusWindow selects the unique window whose title matches title
	// (the original bidirectional substring semantics). It returns
	// ErrWindowMissing when no window matches and ErrWindowAmbiguous when
	// more than one match has a non-zero size.
	FocusWindow(title string) (WindowInfo, error)
	// ActivateWindow brings hwnd to the foreground. Implementations may
	// retry internally up to the given deadline; ErrWindowMissing means the
	// handle vanished before it could be activated.
	ActivateWindow(hwnd uintptr, deadline time.Time) error
	// CaptureScreen grabs the full virtual screen and stores it as an image
	// file at path. The engine owns the file and removes it after the step.
	CaptureScreen(path string) error
	// MatchTemplate loads the screenshot at screenPath and the template
	// image at templatePath and returns the best template match.
	MatchTemplate(screenPath, templatePath string) (Match, error)
	// MoveMouse moves the cursor to screen coordinates (x,y).
	MoveMouse(x, y int) error
	// MouseClick performs a left (button==1) or right (other values) click.
	MouseClick(button int) error
	// SendKey replays a recorded key sequence, using the original
	// "ctrl+"/"alt+" prefix and "~<rawcode>~" escape semantics.
	SendKey(params string) error
}

// Optional precheck interface: when a Platform implements WindowPrechecker,
// the engine can fail ambiguous/missing windows during the strict preflight
// phase instead of at execution time.
type WindowPrechecker interface {
	// CheckWindow validates that title (empty means "no window required")
	// resolves to exactly one usable window.
	CheckWindow(title string) error
}

// Step is a fully parsed, executable replay action.
type Step struct {
	Item HistoryItem

	// key action
	KeyParams string

	// mouse actions
	TemplateFile string // part/window capture referenced by the TSV
	OffsetX      int
	OffsetY      int
	Button       int // click only: 1 = left, otherwise right
}

// StepStatus values for StepResult.Status. There is deliberately no
// "skipped" value: the transactional engine never silently skips a step.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusAborted = "aborted"
)

// StepResult records the outcome of one executed step. Every step the engine
// touches gets a result, including steps aborted mid-flight.
type StepResult struct {
	Line   int    `json:"line"`
	Device string `json:"device"`
	Pre    string `json:"pre"`
	Aft    string `json:"aft"`
	Params string `json:"params"`
	Status string `json:"status"`
	// Attempts is the number of capture/match attempts performed (mouse steps).
	Attempts int `json:"attempts,omitempty"`
	// ActionX/ActionY are the final screen coordinates used (mouse steps).
	ActionX int `json:"actionX,omitempty"`
	ActionY int `json:"actionY,omitempty"`
	// Error is empty on success.
	Error string `json:"error,omitempty"`
	// StartedAt/FinishedAt are UTC timestamps.
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Replay phases reported by Report.Phase.
const (
	PhasePreflight = "preflight"
	PhaseReplay    = "replay"
	PhaseAborted   = "aborted"
	PhaseDone      = "done"
)

// Report is the verifiable audit trail of one replay transaction.
type Report struct {
	ImportFile string       `json:"importFile"`
	TmpDir     string       `json:"tmpDir"`
	RunDir     string       `json:"runDir"`
	Phase      string       `json:"phase"`
	Status     string       `json:"status"` // "ok" | "failed" | "aborted"
	Total      int          `json:"total"`
	Succeeded  int          `json:"succeeded"`
	Failed     int          `json:"failed"`
	Steps      []StepResult `json:"steps"`
	Error      string       `json:"error,omitempty"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
}

// Config holds all per-run replay options. There is intentionally no package
// level mutable configuration: two Config values never share state.
type Config struct {
	ImportFile string
	TmpBaseDir string        // directory containing the recorded captures
	Threshold  float32       // match confidence threshold
	Try        int           // capture/match and focus attempts
	Wait       time.Duration // delay between attempts and between steps
	KeepTmp    bool          // leave the per-run scratch directory in place
	Debug      bool
	Now        func() time.Time // injectable clock (tests)
	RandSeed   int64            // deterministic run directory names (tests, 0 = random)
}

// Engine runs exactly one replay transaction. Create a fresh Engine per
// invocation; nothing is cached globally between replays.
type Engine struct {
	cfg      Config
	platform Platform
	now      func() time.Time
}

// NewEngine builds an Engine. cfg is copied by value and never mutated.
func NewEngine(cfg Config, p Platform) *Engine {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.Try < 1 {
		cfg.Try = 1
	}
	if cfg.Wait < 0 {
		cfg.Wait = 0
	}
	return &Engine{cfg: cfg, platform: p, now: now}
}
