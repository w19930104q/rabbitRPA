// Package launcher starts the child RPA binary (do.exe).
//
// Unlike the original implementation it never builds a "cmd /C <string>"
// command: every argument is passed as a separate argv element, so paths and
// values containing spaces or shell metacharacters cannot be re-parsed.
package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/yasutakatou/rabbitRPA/internal/assets"
)

// Exit codes produced by the launcher itself. Child exit codes are passed
// through unchanged.
const (
	ExitOK             = 0
	ExitLaunchFailure  = 1
	ExitChildMissing   = 2
	ExitPayloadMissing = 3
)

// Resolver finds the child executable path. Overridable in tests.
type Resolver func(exeName, dir string) (string, error)

// LookPath is the production resolver: an existing file next to the running
// launcher wins (supports directories with spaces), otherwise PATH is used.
func LookPath(exeName, dir string) (string, error) {
	candidate := filepath.Join(dir, exeName)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	found, err := exec.LookPath(exeName)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", exec.ErrNotFound, exeName, err)
	}
	return found, nil
}

// Streams wires the child process to the caller's standard streams.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Options configures one invocation. Nothing is stored globally, so repeated
// calls with different Options never share state.
type Options struct {
	// ExeName is the child binary name ("do.exe" on Windows).
	ExeName string
	// WorkDir is where extracted payloads and a relative child path live.
	WorkDir string
	Args    []string
	Streams Streams
	Resolve Resolver
	// Env is the child environment in "KEY=value" form; nil inherits the
	// launcher's own environment (os.Environ is used at execution time).
	Env []string
	// SkipExtract disables embedded payload extraction (tests / external do.exe).
	SkipExtract bool
}

// EnsurePayloads extracts every embedded payload into WorkDir unless it is
// already present. Existing files are never overwritten (another running
// job may own them), and extraction is all-or-nothing for this invocation:
// a failure removes only files this call created.
func EnsurePayloads(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create work directory: %w", err)
	}
	var created []string
	rollback := func() {
		for _, f := range created {
			_ = os.Remove(f)
		}
	}
	for _, name := range assets.Names {
		target := filepath.Join(workDir, name)
		if _, err := os.Stat(target); err == nil {
			continue // already present; leave foreign/previous files untouched
		} else if !os.IsNotExist(err) {
			rollback()
			return fmt.Errorf("stat payload %s: %w", name, err)
		}
		if assets.IsPlaceholder(name) {
			rollback()
			return fmt.Errorf("%w: %s", assets.ErrPlaceholder, name)
		}
		src, err := assets.Open(name)
		if err != nil {
			rollback()
			return fmt.Errorf("open embedded payload %s: %w", name, err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
		if err != nil {
			src.Close()
			rollback()
			return fmt.Errorf("create payload %s: %w", name, err)
		}
		if _, err := io.Copy(out, src); err != nil {
			out.Close()
			src.Close()
			rollback()
			return fmt.Errorf("write payload %s: %w", name, err)
		}
		if err := out.Close(); err != nil {
			src.Close()
			rollback()
			return fmt.Errorf("close payload %s: %w", name, err)
		}
		if err := src.Close(); err != nil {
			rollback()
			return fmt.Errorf("close embedded payload %s: %w", name, err)
		}
		created = append(created, target)
	}
	return nil
}

// Run resolves and executes the child binary with argv passed verbatim and
// returns the child exit code. No shell is involved on any platform.
func Run(opts Options) (int, error) {
	if opts.ExeName == "" {
		return ExitLaunchFailure, errors.New("launcher: child executable name is empty")
	}
	if opts.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return ExitLaunchFailure, fmt.Errorf("launcher: determine work directory: %w", err)
		}
		opts.WorkDir = wd
	}
	if !opts.SkipExtract {
		if err := EnsurePayloads(opts.WorkDir); err != nil {
			return ExitPayloadMissing, err
		}
	}
	resolver := opts.Resolve
	if resolver == nil {
		resolver = LookPath
	}
	exePath, err := resolver(opts.ExeName, opts.WorkDir)
	if err != nil {
		return ExitChildMissing, fmt.Errorf("locate child binary: %w", err)
	}

	// argv = [exePath, opts.Args...]; exec does not invoke a shell, so
	// spaces and quotes are preserved element by element.
	cmd := exec.Command(exePath, opts.Args...)
	cmd.Dir = opts.WorkDir
	// nil means "inherit", preserving the parent environment for the child.
	cmd.Env = opts.Env
	cmd.Stdin = opts.Streams.Stdin
	cmd.Stdout = opts.Streams.Stdout
	cmd.Stderr = opts.Streams.Stderr
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil // pass the child's own code through
		}
		return ExitLaunchFailure, fmt.Errorf("start child %s: %w", exePath, err)
	}
	return ExitOK, nil
}

// DefaultExeName returns the conventional child binary name for the platform.
func DefaultExeName() string {
	if runtime.GOOS == "windows" {
		return "do.exe"
	}
	return "do"
}
