// rabbitRPA launcher: extracts the bundled RPA payloads (when embedded) and
// starts the child RPA binary. All arguments are forwarded as separate argv
// elements — there is no "cmd /C" string, so paths with spaces are safe.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/yasutakatou/rabbitRPA/internal/launcher"
)

func main() {
	os.Exit(run())
}

func run() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "launcher: resolve own executable path:", err)
		return launcher.ExitLaunchFailure
	}
	workDir := filepath.Dir(exe)

	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "-debug" || contains(args, "-debug")) {
		fmt.Println("launch>", launcher.DefaultExeName(), quoteArgs(args))
	}

	code, err := launcher.Run(launcher.Options{
		ExeName: launcher.DefaultExeName(),
		WorkDir: workDir,
		Args:    args,
		Streams: launcher.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr},
		// Extraction is skipped automatically when only placeholders are
		// embedded; a real release build embeds the payloads and extracts.
		SkipExtract: hasNoRealPayloads(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launcher:", err)
	}
	return code
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func quoteArgs(args []string) string {
	out := ""
	for _, a := range args {
		out += "[" + a + "] "
	}
	if len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out
}
