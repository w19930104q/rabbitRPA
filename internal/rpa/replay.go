package rpa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Run executes one replay transaction end to end.
//
// The transaction contract is:
//   - Strict preflight validates the TSV, every parameter, every referenced
//     capture and (when the platform supports it) every window title before
//     any desktop action runs.
//   - Every executed step produces a StepResult carrying its original TSV
//     line number. Missing/ambiguous windows, failed captures, missing
//     templates and sub-threshold matches are hard failures; nothing is
//     silently skipped.
//   - The only directory removed on any exit path is the per-run scratch
//     directory created by this invocation. Recorded captures, the TSV and
//     any pre-existing tmp content are never touched.
//   - A cancelled/failed ctx aborts the run with ErrInterrupted.
//
// The returned Report is always populated (including preflight failures) so
// callers can persist it as an audit artifact.
func (e *Engine) Run(ctx context.Context, items []HistoryItem) (report *Report, err error) {
	started := e.now().UTC()
	report = &Report{
		ImportFile: e.cfg.ImportFile,
		TmpDir:     e.cfg.TmpBaseDir,
		Phase:      PhasePreflight,
		Status:     StatusFailed,
		StartedAt:  started,
		Steps:      []StepResult{},
	}
	defer func() {
		report.FinishedAt = e.now().UTC()
		if r := recover(); r != nil {
			report.Status = StatusFailed
			report.Phase = PhaseDone
			if report.Error == "" {
				report.Error = fmt.Sprintf("panic: %v", r)
			}
			err = fmt.Errorf("replay panicked: %v", r)
		}
	}()

	if ctx == nil {
		ctx = context.Background()
	}
	if cerr := ctx.Err(); cerr != nil {
		return report, e.markInterrupted(report, cerr)
	}

	// --- strict preflight -------------------------------------------------
	if info, statErr := os.Stat(e.cfg.TmpBaseDir); statErr != nil {
		report.Error = "tmp directory unavailable: " + statErr.Error()
		return report, fmt.Errorf("%w: tmp directory %q: %v", ErrPreflight, e.cfg.TmpBaseDir, statErr)
	} else if !info.IsDir() {
		report.Error = "tmp path is not a directory: " + e.cfg.TmpBaseDir
		return report, fmt.Errorf("%w: tmp path %q is not a directory", ErrPreflight, e.cfg.TmpBaseDir)
	}

	steps, perr := ParseSteps(items, e.cfg.TmpBaseDir)
	if perr != nil {
		report.Error = perr.Error()
		return report, fmt.Errorf("%w: %v", ErrPreflight, perr)
	}

	if pc, ok := e.platform.(WindowPrechecker); ok {
		if werr := e.precheckWindows(ctx, pc, steps); werr != nil {
			if errors.Is(werr, ErrInterrupted) {
				return report, e.markInterrupted(report, werr)
			}
			report.Error = werr.Error()
			return report, werr
		}
	}

	// --- per-run scratch directory (no global chdir, no shared tmp) -------
	runDir, rerr := e.makeRunDir()
	if rerr != nil {
		report.Error = rerr.Error()
		return report, rerr
	}
	report.RunDir = runDir
	if !e.cfg.KeepTmp {
		// Owned solely by this invocation and removed on every exit path.
		defer func() {
			if rmErr := os.RemoveAll(runDir); rmErr != nil && !os.IsNotExist(rmErr) {
				fmt.Fprintln(os.Stderr, "warning: could not remove run directory", runDir, rmErr)
			}
		}()
	}

	report.Total = len(steps)
	report.Phase = PhaseReplay

	// --- execute every step; first failure aborts the transaction ---------
	for idx := range steps {
		if cerr := ctx.Err(); cerr != nil {
			return report, e.markInterrupted(report, cerr)
		}
		res := e.executeStep(ctx, &steps[idx], runDir)
		report.Steps = append(report.Steps, res)
		if res.Status == StatusOK {
			report.Succeeded++
		} else {
			report.Failed++
			if errors.Is(errFromResult(res), ErrInterrupted) {
				report.Phase = PhaseAborted
				report.Status = StatusAborted
				report.Error = res.Error
				return report, ErrInterrupted
			}
			report.Phase = PhaseDone
			report.Status = StatusFailed
			report.Error = res.Error
			return report, errors.New(res.Error)
		}

		if idx < len(steps)-1 {
			if ierr := interruptibleSleep(ctx, e.cfg.Wait); ierr != nil {
				return report, e.markInterrupted(report, ierr)
			}
		}
	}

	report.Phase = PhaseDone
	report.Status = StatusOK
	return report, nil
}

func errFromResult(res StepResult) error {
	if res.Error == "" {
		return nil
	}
	return errors.New(res.Error)
}

// Preflight validates only the Pre titles (the titles required at execution
// time; Aft is a fallback and is evaluated by the same rule at runtime).
func (e *Engine) precheckWindows(ctx context.Context, pc WindowPrechecker, steps []Step) error {
	seen := map[string]bool{}
	for _, st := range steps {
		if st.Item.Pre == "" || seen[st.Item.Pre] {
			continue
		}
		seen[st.Item.Pre] = true
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("%w: %v", ErrInterrupted, cerr)
		}
		if err := pc.CheckWindow(st.Item.Pre); err != nil {
			return fmt.Errorf("%w: line %d: window %q: %v",
				ErrPreflight, st.Item.Line, st.Item.Pre, err)
		}
	}
	return nil
}

func (e *Engine) markInterrupted(report *Report, cause error) error {
	if report.Phase == "" {
		report.Phase = PhaseAborted
	}
	report.Status = StatusAborted
	if cause != nil {
		report.Error = cause.Error()
	} else {
		report.Error = ErrInterrupted.Error()
	}
	return ErrInterrupted
}

// makeRunDir creates a unique scratch directory for this invocation only.
func (e *Engine) makeRunDir() (string, error) {
	base := filepath.Join(e.cfg.TmpBaseDir, "replay-run-"+e.runSuffix())
	// MkdirAll with a random suffix; loop once more on the astronomically
	// unlikely name collision instead of reusing someone else's directory.
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := os.Stat(base); os.IsNotExist(err) {
			if merr := os.Mkdir(base, 0o700); merr == nil {
				return base, nil
			} else if !os.IsExist(merr) {
				return "", fmt.Errorf("create run directory: %w", merr)
			}
		}
	}
	return "", fmt.Errorf("create run directory: could not allocate unique directory under %q", e.cfg.TmpBaseDir)
}

func (e *Engine) runSuffix() string {
	if e.cfg.RandSeed != 0 {
		// Deterministic, non-crypto naming for reproducible tests.
		n := new(big.Int).Exp(big.NewInt(2), big.NewInt(40), nil)
		v, _ := rand.Int(seededReader(e.cfg.RandSeed), n)
		return fmt.Sprintf("%d-%010x", e.now().UnixNano(), v.Int64())
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", e.now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", e.now().UnixNano(), hex.EncodeToString(b[:]))
}

func (e *Engine) executeStep(ctx context.Context, st *Step, runDir string) (res StepResult) {
	res = StepResult{
		Line:      st.Item.Line,
		Device:    st.Item.Device,
		Pre:       st.Item.Pre,
		Aft:       st.Item.Aft,
		Params:    st.Item.Params,
		StartedAt: e.now().UTC(),
		Status:    StatusFailed,
	}
	defer func() {
		res.FinishedAt = e.now().UTC()
		if r := recover(); r != nil {
			res.Status = StatusFailed
			res.Error = fmt.Sprintf("line %d: panic: %v", st.Item.Line, r)
		}
	}()

	fmt.Printf("[line %d] Device: %s Pre: %q Aft: %q Params: %q\n",
		st.Item.Line, st.Item.Device, st.Item.Pre, st.Item.Aft, st.Item.Params)

	if err := e.focusForStep(ctx, st); err != nil {
		res.Error = err.Error()
		return res
	}

	switch st.Item.Device {
	case DeviceKey:
		if err := e.platform.SendKey(st.KeyParams); err != nil {
			res.Error = fmt.Sprintf("line %d: key input failed: %v", st.Item.Line, err)
			return res
		}
	case DeviceClick, DeviceMove:
		ok, err := e.runMouseStep(ctx, st, runDir, &res)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		if !ok {
			res.Error = fmt.Sprintf("line %d: template match confidence below threshold %g after %d attempt(s)",
				st.Item.Line, e.cfg.Threshold, e.cfg.Try)
			return res
		}
	default:
		// ParseSteps already rejects unknown devices; keep a hard guard.
		res.Error = fmt.Sprintf("line %d: unsupported device %q", st.Item.Line, st.Item.Device)
		return res
	}

	res.Status = StatusOK
	return res
}

// focusForStep reproduces the original Pre-first/Aft-fallback window rule but
// turns missing or ambiguous matches into explicit failures.
func (e *Engine) focusForStep(ctx context.Context, st *Step) error {
	if st.Item.Pre == "" {
		return nil
	}
	win, err := e.platform.FocusWindow(st.Item.Pre)
	if err == nil {
		return e.activate(ctx, win.Handle, st.Item.Line, "pre")
	}
	if !errors.Is(err, ErrWindowMissing) || st.Item.Aft == "" {
		return fmt.Errorf("line %d: pre window %q: %w", st.Item.Line, st.Item.Pre, err)
	}

	win, err = e.platform.FocusWindow(st.Item.Aft)
	if err != nil {
		return fmt.Errorf("line %d: pre window %q missing and aft window %q: %w",
			st.Item.Line, st.Item.Pre, st.Item.Aft, err)
	}
	return e.activate(ctx, win.Handle, st.Item.Line, "aft")
}

func (e *Engine) activate(ctx context.Context, hwnd uintptr, line int, role string) error {
	deadline := e.now().Add(time.Duration(e.cfg.Try) * e.maxWait())
	err := e.platform.ActivateWindow(hwnd, deadline)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInterrupted) || ctx.Err() != nil {
		return ErrInterrupted
	}
	return fmt.Errorf("line %d: %s window activation failed: %w", line, role, err)
}

func (e *Engine) maxWait() time.Duration {
	if e.cfg.Wait <= 0 {
		return time.Millisecond
	}
	return e.cfg.Wait
}

// runMouseStep performs the capture -> match -> move -> click cycle, retrying
// up to cfg.Try times. Each attempt uses a fresh screenshot owned by this run.
func (e *Engine) runMouseStep(ctx context.Context, st *Step, runDir string, res *StepResult) (bool, error) {
	templatePath := filepath.Join(e.cfg.TmpBaseDir, st.TemplateFile)

	var last Match
	for attempt := 1; attempt <= e.cfg.Try; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return false, ErrInterrupted
		}
		res.Attempts = attempt

		shot, err := e.captureOnce(runDir, attempt)
		if err != nil {
			return false, fmt.Errorf("line %d: %w", st.Item.Line, err)
		}
		// The screenshot is scratch data: always remove it, even on failure.
		matched, merr := e.matchOnce(shot, templatePath)
		if rmErr := os.Remove(shot); rmErr != nil && !os.IsNotExist(rmErr) && e.cfg.Debug {
			fmt.Fprintln(os.Stderr, "warning: remove screenshot failed:", rmErr)
		}
		if merr != nil {
			if errors.Is(merr, ErrInterrupted) || ctx.Err() != nil {
				return false, ErrInterrupted
			}
			// A vanished template is a hard, accurate failure.
			return false, fmt.Errorf("line %d: %w", st.Item.Line, merr)
		}
		last = matched
		if e.cfg.Debug {
			fmt.Printf("line %d attempt %d: confidence=%g point=%v threshold=%g\n",
				st.Item.Line, attempt, matched.Confidence, matched.Point, e.cfg.Threshold)
		}
		if matched.Confidence > e.cfg.Threshold {
			actionX := matched.Point.X + st.OffsetX
			actionY := matched.Point.Y + st.OffsetY
			res.ActionX, res.ActionY = actionX, actionY
			if err := e.platform.MoveMouse(actionX, actionY); err != nil {
				return false, fmt.Errorf("line %d: mouse move failed: %w", st.Item.Line, err)
			}
			if st.Item.Device == DeviceClick {
				if err := e.platform.MouseClick(st.Button); err != nil {
					return false, fmt.Errorf("line %d: mouse click failed: %w", st.Item.Line, err)
				}
			}
			return true, nil
		}
		if attempt < e.cfg.Try {
			if err := interruptibleSleep(ctx, e.cfg.Wait); err != nil {
				return false, ErrInterrupted
			}
		}
	}
	_ = last
	return false, nil
}

func (e *Engine) captureOnce(runDir string, attempt int) (string, error) {
	name := fmt.Sprintf("screen-%d-%s.bmp", attempt, randName(6))
	path := filepath.Join(runDir, name)
	if err := e.platform.CaptureScreen(path); err != nil {
		return "", fmt.Errorf("screen capture failed: %w", err)
	}
	if info, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("screen capture produced no file: %w", err)
	} else if info.IsDir() || info.Size() == 0 {
		_ = os.RemoveAll(path)
		return "", fmt.Errorf("screen capture produced an empty file: %s", path)
	}
	return path, nil
}

func (e *Engine) matchOnce(screenPath, templatePath string) (Match, error) {
	if _, err := os.Stat(templatePath); err != nil {
		if os.IsNotExist(err) {
			return Match{}, fmt.Errorf("template image missing: %s: %w", templatePath, err)
		}
		return Match{}, fmt.Errorf("template image unreadable: %s: %w", templatePath, err)
	}
	m, err := e.platform.MatchTemplate(screenPath, templatePath)
	if err != nil {
		return Match{}, fmt.Errorf("template matching failed: %w", err)
	}
	return m, nil
}

// WriteReport persists the JSON audit report. The report lives outside the
// per-run scratch directory so it survives cleanup.
func WriteReport(path string, report *Report) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func interruptibleSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
