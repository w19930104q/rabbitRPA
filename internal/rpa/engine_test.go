package rpa_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasutakatou/rabbitRPA/internal/rpa"
	"github.com/yasutakatou/rabbitRPA/internal/rpa/fake"
)

// ---------------------------------------------------------------- fixtures

func writeImage(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 9, G: 8, B: 7, A: 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode image: %v", err)
	}
}

// setupWorkspace builds a tmp directory containing the captures referenced by
// the fixed TSVs and returns (dir, tsv path, tmp dir).
func setupWorkspace(t *testing.T, tsv string) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"all01.bmp", "part01.bmp", "move01.bmp"} {
		writeImage(t, filepath.Join(tmp, name))
	}
	tsvPath := filepath.Join(root, "input.tsv")
	if err := os.WriteFile(tsvPath, []byte(tsv), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, tsvPath, tmp
}

const happyTSV = "key\tNotepad\tNotepad\tctrl+a\n" +
	"click\tNotepad\tNotepad\tall01.bmp;part01.bmp;12;34;1\n" +
	"move\tNotepad\t\tmove01.bmp;7;9\n"

func windows() []rpa.WindowInfo {
	return []rpa.WindowInfo{
		{Handle: 1001, Title: "Untitled - Notepad", Rect: image.Rect(0, 0, 800, 600)},
		{Handle: 1002, Title: "Calculator", Rect: image.Rect(0, 600, 200, 800)},
	}
}

func newEngine(cfg rpa.Config, p rpa.Platform) *rpa.Engine {
	if cfg.Try == 0 {
		cfg.Try = 3
	}
	if cfg.Wait == 0 {
		cfg.Wait = time.Millisecond
	}
	return rpa.NewEngine(cfg, p)
}

// ------------------------------------------------------------- TSV parsing

func TestReadTSVRejectsMalformedLinesWithLineNumbers(t *testing.T) {
	root := t.TempDir()
	bad := "key\tA\tB\tx\n" + // line 1 ok
		"click\tonly\ttwo\n" + // line 2: 3 fields
		"\n" + // line 3 blank
		"move\tA\tB\tm.bmp;1;2\n" // line 4 ok
	path := filepath.Join(root, "broken.tsv")
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := rpa.ReadTSV(path)
	if err == nil {
		t.Fatal("expected malformed TSV to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line 2") || !strings.Contains(msg, "line 3") {
		t.Fatalf("error must carry both offending line numbers, got: %v", err)
	}
}

func TestReadTSVMissingAndEmpty(t *testing.T) {
	_, err := rpa.ReadTSV(filepath.Join(t.TempDir(), "nope.tsv"))
	if err == nil {
		t.Fatal("missing file must error")
	}
	empty := filepath.Join(t.TempDir(), "empty.tsv")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rpa.ReadTSV(empty); err == nil {
		t.Fatal("file without any valid step must error")
	}
}

func TestParseStepsMissingImageFailsPreflight(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the part image referenced by line 2.
	if err := os.Remove(filepath.Join(tmp, "part01.bmp")); err != nil {
		t.Fatal(err)
	}
	_, err = rpa.ParseSteps(items, tmp)
	if err == nil || !strings.Contains(err.Error(), "line 2") ||
		!strings.Contains(err.Error(), "part01.bmp") {
		t.Fatalf("expected line-2 missing-image error, got: %v", err)
	}
}

func TestParseStepsRejectsPathTraversal(t *testing.T) {
	tsv := "click\tA\tB\tall01.bmp;..\\escape.bmp;1;2;1\n"
	_, tsvPath, tmp := setupWorkspace(t, tsv)
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpa.ParseSteps(items, tmp); err == nil ||
		!strings.Contains(err.Error(), "line 1") {
		t.Fatalf("traversal reference must fail with line number, got: %v", err)
	}
}

func TestParseStepsBadNumbersAndDevice(t *testing.T) {
	cases := map[string]string{
		"click bad x":    "click\tA\tB\tall01.bmp;part01.bmp;xx;2;1\n",
		"click fields":   "click\tA\tB\tpart01.bmp;1;2\n",
		"move bad y":     "move\tA\tB\tmove01.bmp;1;yy\n",
		"unknown device": "shake\tA\tB\twhatever\n",
		"empty key":      "key\tA\tB\t\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, tsvPath, tmp := setupWorkspace(t, body)
			items, err := rpa.ReadTSV(tsvPath)
			if err != nil {
				t.Fatalf("ReadTSV: %v", err)
			}
			if _, err := rpa.ParseSteps(items, tmp); err == nil {
				t.Fatal("expected parse failure")
			}
		})
	}
}

// ------------------------------------------------------- happy transaction

func TestReplayHappyPathRecordsAllStepsAndLineNumbers(t *testing.T) {
	root, tsvPath, tmp := setupWorkspace(t, happyTSV)
	reportPath := filepath.Join(root, "report.json")
	p := fake.New(fake.WindowScript{Windows: windows()}, []fake.MatchScript{
		{Match: rpa.Match{Confidence: 0.95, Point: image.Pt(100, 200)}},
		{Match: rpa.Match{Confidence: 0.9, Point: image.Pt(50, 60)}},
	})
	cfg := rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Threshold: 0.1, Try: 3, Wait: time.Millisecond}
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := newEngine(cfg, p).Run(context.Background(), items)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if rep.Status != "ok" || rep.Total != 3 || rep.Succeeded != 3 || rep.Failed != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	lines := []int{}
	var ops []string
	for _, s := range rep.Steps {
		lines = append(lines, s.Line)
		ops = append(ops, s.Device+":"+s.Status)
	}
	wantLines := []int{1, 2, 3}
	if len(lines) != len(wantLines) {
		t.Fatalf("line count mismatch: %v", lines)
	}
	for i := range wantLines {
		if lines[i] != wantLines[i] {
			t.Fatalf("line numbers not preserved: %v", lines)
		}
	}
	_ = ops
	calls := p.Calls()
	if !containsCall(calls, "key", "ctrl+a") {
		t.Fatalf("key call missing: %+v", calls)
	}
	// click: match point (100,200) + offset (12,34) => (112,234)
	if !containsPoint(p.Moves, image.Pt(112, 234)) {
		t.Fatalf("click coordinates wrong: %+v", p.Moves)
	}
	// move: (50,60) + (7,9) => (57,69); no click expected for move steps
	if !containsPoint(p.Moves, image.Pt(57, 69)) {
		t.Fatalf("move coordinates wrong: %+v", p.Moves)
	}
	if len(p.Clicks) != 1 || p.Clicks[0].Button != 1 || p.Clicks[0].Point != (image.Pt(112, 234)) {
		t.Fatalf("click trace wrong: %+v", p.Clicks)
	}

	// Report content is returned to the caller; the CLI persists it via
	// WriteReport (covered separately). Verify that round trip explicitly.
	if err := rpa.WriteReport(reportPath, rep); err != nil {
		t.Fatalf("write report: %v", err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	var fromDisk rpa.Report
	if err := json.Unmarshal(data, &fromDisk); err != nil {
		t.Fatal(err)
	}
	if fromDisk.Status != "ok" || len(fromDisk.Steps) != 3 {
		t.Fatalf("persisted report mismatch: %+v", fromDisk)
	}

	// Scratch directory must be gone; recorded captures must remain.
	if _, err := os.Stat(rep.RunDir); !os.IsNotExist(err) {
		t.Fatalf("run directory not cleaned up: %s", rep.RunDir)
	}
	for _, name := range []string{"all01.bmp", "part01.bmp", "move01.bmp"} {
		if _, err := os.Stat(filepath.Join(tmp, name)); err != nil {
			t.Fatalf("recorded capture %s must survive: %v", name, err)
		}
	}
}

// ------------------------------------------------------- failure semantics

func TestReplayMissingWindowFailsAtPreflightWithLineNumber(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	p := fake.New(fake.WindowScript{Windows: []rpa.WindowInfo{
		{Handle: 1, Title: "Calculator", Rect: image.Rect(0, 0, 10, 10)},
	}}, nil)
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Try: 1, Wait: time.Millisecond}, p).Run(context.Background(), items)
	if err == nil {
		t.Fatal("missing window must fail")
	}
	if rep.Phase != rpa.PhasePreflight {
		t.Fatalf("window failure belongs to preflight, got phase %q", rep.Phase)
	}
	if !strings.Contains(rep.Error, "line 1") ||
		!strings.Contains(rep.Error, rpa.ErrWindowMissing.Error()) {
		t.Fatalf("expected line-1 missing-window error, got %q", rep.Error)
	}
	if len(rep.Steps) != 0 || rep.Succeeded != 0 {
		t.Fatal("no step may execute when preflight fails")
	}
	for _, c := range p.Calls() {
		if c.Op == "move" || c.Op == "click" || c.Op == "key" {
			t.Fatalf("desktop action leaked during preflight: %+v", c)
		}
	}
}

// noPrecheckPlatform wraps the fake but hides WindowPrechecker, so window
// failures surface at execution time on the step result instead. Only the
// eight Platform methods are exposed deliberately.
type noPrecheckPlatform struct {
	inner *fake.Platform
}

func (n noPrecheckPlatform) ListWindows() ([]rpa.WindowInfo, error) { return n.inner.ListWindows() }
func (n noPrecheckPlatform) FocusWindow(title string) (rpa.WindowInfo, error) {
	return n.inner.FocusWindow(title)
}
func (n noPrecheckPlatform) ActivateWindow(h uintptr, d time.Time) error {
	return n.inner.ActivateWindow(h, d)
}
func (n noPrecheckPlatform) CaptureScreen(path string) error { return n.inner.CaptureScreen(path) }
func (n noPrecheckPlatform) MatchTemplate(a, b string) (rpa.Match, error) {
	return n.inner.MatchTemplate(a, b)
}
func (n noPrecheckPlatform) MoveMouse(x, y int) error { return n.inner.MoveMouse(x, y) }
func (n noPrecheckPlatform) MouseClick(b int) error   { return n.inner.MouseClick(b) }
func (n noPrecheckPlatform) SendKey(p string) error   { return n.inner.SendKey(p) }

func TestReplayMissingWindowAtExecutionFailsStep(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	p := noPrecheckPlatform{inner: fake.New(fake.WindowScript{Windows: []rpa.WindowInfo{
		{Handle: 1, Title: "Calculator", Rect: image.Rect(0, 0, 10, 10)},
	}}, nil)}
	rep := runExpectFailure(t, p, tsvPath, tmp)
	if len(rep.Steps) != 1 || rep.Steps[0].Status != rpa.StatusFailed {
		t.Fatalf("expected one failed step, got %+v", rep.Steps)
	}
	if !strings.Contains(rep.Steps[0].Error, "line 1") ||
		!strings.Contains(rep.Steps[0].Error, rpa.ErrWindowMissing.Error()) {
		t.Fatalf("expected missing-window failure on line 1, got %+v", rep.Steps[0])
	}
	if rep.Succeeded != 0 {
		t.Fatal("transaction must abort on first failure, no silent skip")
	}
}

func TestReplayAmbiguousWindowFails(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	p := noPrecheckPlatform{inner: fake.New(fake.WindowScript{Windows: []rpa.WindowInfo{
		{Handle: 1, Title: "Notepad - doc1", Rect: image.Rect(0, 0, 5, 5)},
		{Handle: 2, Title: "Notepad - doc2", Rect: image.Rect(0, 0, 5, 5)},
	}}, nil)}
	rep := runExpectFailure(t, p, tsvPath, tmp)
	if !strings.Contains(rep.Steps[0].Error, "ambiguous") ||
		!strings.Contains(rep.Steps[0].Error, "line 1") {
		t.Fatalf("ambiguity must be an explicit line-1 failure, got %+v", rep)
	}
}

func TestReplayWindowPrecheckFailsBeforeAnyAction(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	p := fake.New(fake.WindowScript{Windows: nil}, nil) // fake implements WindowPrechecker
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Try: 1, Wait: time.Millisecond}, p).Run(context.Background(), items)
	if err == nil {
		t.Fatal("preflight must fail")
	}
	if rep.Phase != rpa.PhasePreflight || len(rep.Steps) != 0 {
		t.Fatalf("no step may execute after preflight failure: %+v", rep)
	}
	for _, c := range p.Calls() {
		if c.Op == "move" || c.Op == "click" || c.Op == "key" {
			t.Fatalf("desktop action leaked during preflight: %+v", c)
		}
	}
}

func TestReplayCaptureFailureFailsAccurately(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t,
		"click\tNotepad\tNotepad\tall01.bmp;part01.bmp;1;2;1\n")
	p := fake.New(fake.WindowScript{Windows: windows()}, nil)
	p.SetCaptureFail(1)
	rep := runExpectFailure(t, p, tsvPath, tmp)
	if !strings.Contains(rep.Steps[0].Error, "screen capture failed") {
		t.Fatalf("capture failure must be surfaced, got %+v", rep.Steps[0])
	}
}

func TestReplayThresholdFailureRetriesAndFails(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t,
		"move\tNotepad\t\tmove01.bmp;1;2\n")
	p := fake.New(fake.WindowScript{Windows: windows()}, []fake.MatchScript{
		{Match: rpa.Match{Confidence: 0.05}},
		{Match: rpa.Match{Confidence: 0.09}},
		{Match: rpa.Match{Confidence: 0.01}},
	})
	rep := runExpectFailureWithTry(t, p, tsvPath, tmp, 3)
	if rep.Steps[0].Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", rep.Steps[0].Attempts)
	}
	if !strings.Contains(rep.Steps[0].Error, "below threshold") {
		t.Fatalf("threshold failure expected, got %+v", rep.Steps[0])
	}
	if len(p.Moves) != 0 {
		t.Fatalf("no mouse movement allowed below threshold: %+v", p.Moves)
	}
}

func TestReplayTemplateVanishedAtRuntimeFails(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t,
		"move\tNotepad\t\tmove01.bmp;1;2\n")
	fp := fake.New(fake.WindowScript{Windows: windows()}, nil)
	// Preflight passes (template present), then the capture wrapper deletes
	// the template so the very first MatchTemplate fails at runtime.
	template := filepath.Join(tmp, "move01.bmp")
	p := &captureThenDelete{Platform: noPrecheckPlatform{inner: fp}, template: template}
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpa.ParseSteps(items, tmp); err != nil {
		t.Fatalf("preflight parse: %v", err)
	}
	rep, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Threshold: 0.1, Try: 1, Wait: time.Millisecond}, p).Run(context.Background(), items)
	if err == nil {
		t.Fatal("runtime missing template must fail")
	}
	if len(rep.Steps) != 1 || !strings.Contains(rep.Steps[0].Error, "template image missing") {
		t.Fatalf("got %+v", rep.Steps)
	}
}

// captureThenDelete removes a file after the first successful screenshot,
// emulating a template capture that disappears between preflight and match.
type captureThenDelete struct {
	rpa.Platform
	template string
	done     bool
}

func (c *captureThenDelete) CaptureScreen(path string) error {
	if err := c.Platform.CaptureScreen(path); err != nil {
		return err
	}
	if !c.done {
		c.done = true
		if err := os.Remove(c.template); err != nil {
			return err
		}
	}
	return nil
}

func TestReplayInterruptedReturnsAbortedAndCleansUp(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	p := fake.New(fake.WindowScript{Windows: windows()}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before start
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Try: 1, Wait: time.Millisecond}, p).Run(ctx, items)
	if !errors.Is(err, rpa.ErrInterrupted) || rep.Status != "aborted" {
		t.Fatalf("expected abort, got %+v err=%v", rep, err)
	}
	if rep.RunDir != "" {
		if _, err := os.Stat(rep.RunDir); !os.IsNotExist(err) {
			t.Fatal("interrupted run must still clean its scratch directory")
		}
	}
}

func TestRepeatedRunsDoNotShareStateOrTmp(t *testing.T) {
	_, tsvPath, tmp := setupWorkspace(t, happyTSV)
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	var runDirs []string
	for i := 0; i < 3; i++ {
		p := fake.New(fake.WindowScript{Windows: windows()}, nil)
		rep, err := rpa.NewEngine(rpa.Config{
			ImportFile: tsvPath, TmpBaseDir: tmp, Threshold: 0.1,
			Try: 1, Wait: time.Millisecond, RandSeed: int64(i + 1),
			Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
		}, p).Run(context.Background(), items)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		runDirs = append(runDirs, rep.RunDir)
		if _, err := os.Stat(rep.RunDir); !os.IsNotExist(err) {
			t.Fatalf("run %d scratch dir remains", i)
		}
	}
	if runDirs[0] == runDirs[1] || runDirs[1] == runDirs[2] {
		t.Fatalf("run directories must be unique per invocation: %v", runDirs)
	}
	// No leftover replay-run directories at all.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "replay-run-") {
			t.Fatalf("stale scratch directory found: %s", e.Name())
		}
	}
}

func TestPreExistingTmpFilesAreNeverRemoved(t *testing.T) {
	root, tsvPath, tmp := setupWorkspace(t, happyTSV)
	_ = root
	staleDir := filepath.Join(tmp, "replay-run-stale-foreign")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(staleDir, "leftover.bmp")
	if err := os.WriteFile(staleFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := fake.New(fake.WindowScript{Windows: windows()}, nil)
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Threshold: 0.1, Try: 1, Wait: time.Millisecond}, p).Run(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleFile); err != nil {
		t.Fatalf("foreign tmp content was removed: %v", err)
	}
}

// ------------------------------------------------------- export round-trip

func TestExportHistoryKeepsRecordedFormat(t *testing.T) {
	items := []rpa.HistoryItem{
		{Device: "click", Pre: "A", Aft: "B", Params: "all.bmp;h1;part.bmp;h2;11;22;1"},
		{Device: "move", Pre: "A", Aft: "", Params: "mv.bmp;h3;5;6"},
		{Device: "key", Pre: "A", Aft: "B", Params: "ctrl+c"},
	}
	var buf bytes.Buffer
	if err := rpa.ExportHistory(&buf, items); err != nil {
		t.Fatal(err)
	}
	want := "click\tA\tB\tall.bmp;part.bmp;11;22;1\n" +
		"move\tA\t\tmv.bmp;5;6\n" +
		"key\tA\tB\tctrl+c\n"
	if buf.String() != want {
		t.Fatalf("export format drift:\n got %q\nwant %q", buf.String(), want)
	}
	// The exported replay TSV must parse back cleanly when images exist.
	tmp := t.TempDir()
	for _, n := range []string{"all.bmp", "part.bmp", "mv.bmp"} {
		writeImage(t, filepath.Join(tmp, n))
	}
	path := filepath.Join(tmp, "exported.tsv")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	reimported, err := rpa.ReadTSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpa.ParseSteps(reimported, tmp); err != nil {
		t.Fatalf("exported TSV must re-validate: %v", err)
	}
}

// ----------------------------------------------------------------- helpers

func runExpectFailure(t *testing.T, p rpa.Platform, tsvPath, tmp string) *rpa.Report {
	t.Helper()
	return runExpectFailureWithTry(t, p, tsvPath, tmp, 2)
}

func runExpectFailureWithTry(t *testing.T, p rpa.Platform, tsvPath, tmp string, try int) *rpa.Report {
	t.Helper()
	items, err := rpa.ReadTSV(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := newEngine(rpa.Config{ImportFile: tsvPath, TmpBaseDir: tmp,
		Threshold: 0.5, Try: try, Wait: time.Millisecond}, p).Run(context.Background(), items)
	if err == nil {
		t.Fatalf("expected failure, report=%+v", rep)
	}
	return rep
}

func containsCall(calls []fake.Call, op, arg string) bool {
	for _, c := range calls {
		if c.Op == op && c.Arg == arg {
			return true
		}
	}
	return false
}

func containsPoint(pts []image.Point, want image.Point) bool {
	for _, p := range pts {
		if p == want {
			return true
		}
	}
	return false
}
