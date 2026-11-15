package launcher_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yasutakatou/rabbitRPA/internal/launcher"
)

// writeFakeDo creates an executable that records every argv element (NUL
// separated) into the file named by RABBITRPA_FAKE_OUT, then exits with the
// code in RABBITRPA_FAKE_EXIT. It contains no shell interpretation.
func writeFakeDo(t *testing.T, dir string) string {
	t.Helper()
	name := "do"
	if runtime.GOOS == "windows" {
		name = "do.exe"
	}
	path := filepath.Join(dir, name)
	script := `#!/bin/sh
: "${RABBITRPA_FAKE_OUT:?missing env}"
first=1
for a in "$@"; do
  if [ "$first" -eq 0 ]; then printf '\000' >> "$RABBITRPA_FAKE_OUT"; fi
  printf '%s' "$a" >> "$RABBITRPA_FAKE_OUT"
  first=0
done
exit "${RABBITRPA_FAKE_EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake do: %v", err)
	}
	return path
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return []string{}
	}
	return strings.Split(string(data), "\x00")
}

func TestRunArgvIntegrityAndExitCodes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work dir")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeDo(t, root)
	outFile := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("RABBITRPA_FAKE_OUT", outFile)

	args := []string{"-replay", "-import=my input.tsv", "-tmpDir=tmp data", "-try=3"}
	code, err := launcher.Run(launcher.Options{
		ExeName:     launcher.DefaultExeName(),
		WorkDir:     root,
		Args:        args,
		SkipExtract: true,
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	got := readArgv(t, outFile)
	if len(got) != len(args) {
		t.Fatalf("argv element count drift: got %d want %d (%v)", len(got), len(args), got)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Fatalf("argv[%d] drift: got %q want %q; full=%v", i, got[i], args[i], got)
		}
	}

	// Non-zero child exit code must be passed through, not collapsed to 0.
	t.Setenv("RABBITRPA_FAKE_EXIT", "7")
	code, err = launcher.Run(launcher.Options{
		ExeName:     launcher.DefaultExeName(),
		WorkDir:     root,
		Args:        []string{"-replay"},
		SkipExtract: true,
	})
	if err != nil {
		t.Fatalf("child failure is not a launcher error: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code not propagated: got %d want 7", code)
	}
}

func TestRunMissingChildFailsExplicitly(t *testing.T) {
	root := t.TempDir()
	resolver := func(_, _ string) (string, error) {
		return "", os.ErrNotExist
	}
	code, err := launcher.Run(launcher.Options{
		ExeName:     "do.exe",
		WorkDir:     root,
		Args:        []string{"-replay"},
		SkipExtract: true,
		Resolve:     resolver,
	})
	if err == nil || code != launcher.ExitChildMissing {
		t.Fatalf("expected ExitChildMissing, code=%d err=%v", code, err)
	}
}

func TestRepeatedRunsAreIndependent(t *testing.T) {
	root := t.TempDir()
	writeFakeDo(t, root)
	outA := filepath.Join(t.TempDir(), "a.log")
	outB := filepath.Join(t.TempDir(), "b.log")

	for i := 0; i < 2; i++ {
		out := outA
		args := []string{"-replay", "-import=a.tsv"}
		if i == 1 {
			out = outB
			args = []string{"-replay", "-import=b file.tsv", "-debug"}
		}
		t.Setenv("RABBITRPA_FAKE_OUT", out)
		code, err := launcher.Run(launcher.Options{
			ExeName:     launcher.DefaultExeName(),
			WorkDir:     root,
			Args:        args,
			SkipExtract: true,
		})
		if err != nil || code != 0 {
			t.Fatalf("run %d: code=%d err=%v", i, code, err)
		}
	}
	if got := readArgv(t, outA); len(got) != 2 || got[1] != "-import=a.tsv" {
		t.Fatalf("run A state leaked: %v", got)
	}
	if got := readArgv(t, outB); len(got) != 3 || got[1] != "-import=b file.tsv" {
		t.Fatalf("run B state leaked: %v", got)
	}
}

func TestLookPathPrefersSiblingExecutable(t *testing.T) {
	root := t.TempDir()
	writeFakeDo(t, root)
	got, err := launcher.LookPath(launcher.DefaultExeName(), root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != root {
		t.Fatalf("expected sibling resolution, got %q", got)
	}
}
