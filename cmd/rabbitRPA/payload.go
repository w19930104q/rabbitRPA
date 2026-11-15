package main

import "github.com/yasutakatou/rabbitRPA/internal/assets"

// hasNoRealPayloads reports whether the build only carries placeholder
// payloads (e.g. an open-source checkout without the embedded binaries).
// In that case extraction is skipped and the child is resolved from PATH or
// from next to the launcher.
func hasNoRealPayloads() bool {
	for _, name := range assets.Names {
		if !assets.IsPlaceholder(name) {
			return false
		}
	}
	return true
}
