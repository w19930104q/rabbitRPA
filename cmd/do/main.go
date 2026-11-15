// do is the RPA worker binary. Record mode is the original Windows-only
// flow; replay mode runs the transactional engine and exits non-zero on any
// missing, ambiguous or failed step.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yasutakatou/rabbitRPA/internal/rpa"
	"github.com/yasutakatou/rabbitRPA/internal/rpa/fake"
)

// Exit codes. Zero means the whole transaction was verified successful.
const (
	exitOK            = 0
	exitReplayFailure = 1
	exitInterrupt     = 2
	exitUsage         = 3
	exitPlatform      = 4
)

type options struct {
	list      bool
	replay    bool
	record    bool
	export    string
	importArg string
	debug     bool
	threshold float64
	move      float64
	try       int
	wait      int
	exitCode  int
	tmpDir    string
	report    string
	keepTmp   bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	var o options
	fs := flag.NewFlagSet("do", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&o.list, "list", false, "listing window titles and exit")
	fs.BoolVar(&o.replay, "replay", false, "replay mode (true is enable)")
	fs.BoolVar(&o.record, "record", true, "recording mode (true is enable)")
	fs.StringVar(&o.export, "export", "output.tsv", "export file name")
	fs.StringVar(&o.importArg, "import", "input.tsv", "import file name")
	fs.BoolVar(&o.debug, "debug", false, "debug mode (true is enable)")
	fs.Float64Var(&o.threshold, "threshold", 0.1, "same window threshold")
	fs.Float64Var(&o.move, "move", 50, "mouse move record threshold")
	fs.IntVar(&o.try, "try", 10, "error and try counter")
	fs.IntVar(&o.wait, "wait", 250, "loop wait Millisecond")
	fs.IntVar(&o.exitCode, "exitCode", 27, "recording mode to exit ascii key code")
	fs.StringVar(&o.tmpDir, "tmpDir", "tmp", "temporary directory name")
	fs.StringVar(&o.report, "report", "replay-report.json", "replay JSON report path (empty disables)")
	fs.BoolVar(&o.keepTmp, "keepTmp", false, "keep the per-run scratch directory for inspection")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if o.try < 1 {
		fmt.Fprintln(os.Stderr, "-try must be >= 1")
		return exitUsage
	}
	if o.wait < 0 {
		fmt.Fprintln(os.Stderr, "-wait must be >= 0")
		return exitUsage
	}

	platform, err := selectPlatform(&o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitPlatform
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}

	if o.list {
		return listWindows(platform)
	}

	// Paths are built with filepath.Join and passed as argv, never via a
	// shell string, so spaces in cwd/tmpDir/import names are harmless.
	tmpBase := filepath.Join(cwd, o.tmpDir)
	importPath := filepath.Join(cwd, o.importArg)
	reportPath := ""
	if o.report != "" {
		if filepath.IsAbs(o.report) {
			reportPath = o.report
		} else {
			reportPath = filepath.Join(cwd, o.report)
		}
	}

	if o.debug {
		printOptions(o, tmpBase)
	}

	if !o.replay {
		return runRecording(&o, cwd, tmpBase, platform)
	}
	return runReplay(&o, platform, tmpBase, importPath, reportPath)
}

func selectPlatform(o *options) (rpa.Platform, error) {
	if os.Getenv("RABBITRPA_FAKE") == "1" {
		// Acceptance adapter: no real desktop. Windows are supplied through
		// the RABBITRPA_FAKE_WINDOWS env (window titles, '|' separated).
		var wins []rpa.WindowInfo
		for i, title := range splitEnv(os.Getenv("RABBITRPA_FAKE_WINDOWS")) {
			if title == "" {
				continue
			}
			wins = append(wins, rpa.WindowInfo{
				Handle: uintptr(1000 + i),
				Title:  title,
				Rect:   rectOf(i),
			})
		}
		return fake.New(fake.WindowScript{Windows: wins}, nil), nil
	}
	return rpa.NewPlatform(o.try, time.Duration(o.wait)*time.Millisecond, o.debug)
}

func listWindows(p rpa.Platform) int {
	wins, err := p.ListWindows()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list windows:", err)
		return exitPlatform
	}
	for _, w := range wins {
		fmt.Printf("%s : %x\n", w.Title, w.Handle)
	}
	return exitOK
}

func runReplay(o *options, p rpa.Platform, tmpBase, importPath, reportPath string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Reading is part of the strict preflight: missing file -> hard failure.
	items, err := rpa.ReadTSV(importPath)
	report := &rpa.Report{
		ImportFile: importPath,
		TmpDir:     tmpBase,
		Phase:      rpa.PhasePreflight,
		Status:     rpa.StatusFailed,
		StartedAt:  time.Now().UTC(),
		Steps:      []rpa.StepResult{},
	}
	if err != nil {
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC()
		if werr := rpa.WriteReport(reportPath, report); werr != nil {
			fmt.Fprintln(os.Stderr, "warning: write report:", werr)
		}
		fmt.Fprintln(os.Stderr, "preflight:", err)
		return exitReplayFailure
	}

	engine := rpa.NewEngine(rpa.Config{
		ImportFile: importPath,
		TmpBaseDir: tmpBase,
		Threshold:  float32(o.threshold),
		Try:        o.try,
		Wait:       time.Duration(o.wait) * time.Millisecond,
		KeepTmp:    o.keepTmp,
		Debug:      o.debug,
	}, p)

	rep, err := engine.Run(ctx, items)
	if werr := rpa.WriteReport(reportPath, rep); werr != nil {
		fmt.Fprintln(os.Stderr, "warning: write report:", werr)
	}
	fmt.Printf("replay finished: status=%s total=%d succeeded=%d failed=%d runDir=%q\n",
		rep.Status, rep.Total, rep.Succeeded, rep.Failed, rep.RunDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay error:", err)
		if errors.Is(err, rpa.ErrInterrupted) || rep.Status == rpa.StatusAborted {
			return exitInterrupt
		}
		return exitReplayFailure
	}
	return exitOK
}

func printOptions(o options, tmpBase string) {
	fmt.Println(" - - - options - - - ")
	fmt.Println("list: ", o.list)
	fmt.Println("replay: ", o.replay)
	fmt.Println("record: ", o.record)
	fmt.Println("export: ", o.export)
	fmt.Println("import: ", o.importArg)
	fmt.Println("debug: ", o.debug)
	fmt.Println("threshold: ", o.threshold)
	fmt.Println("move: ", o.move)
	fmt.Println("try: ", o.try)
	fmt.Println("wait: ", o.wait)
	fmt.Println("exitCode: ", o.exitCode)
	fmt.Println("tmpDir: ", tmpBase)
	fmt.Println("report: ", o.report)
	fmt.Println(" - - - - - - - - - ")
}

func splitEnv(v string) []string {
	if v == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] == '|' {
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	out = append(out, v[start:])
	return out
}
