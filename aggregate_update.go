package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var errAggregateUpdateUnsupported = errors.New("unsupported aggregate spec: go2spec_combine is defined; the legacy updater only updates one Version and Source0")

// rejectAggregateUpdate checks literal definitions only. It never evaluates
// values, conditionals, RPM macros, shell or Lua. A definition is enough to
// reject the spec, even if its value is empty or zero. Commented explanations
// and longer macro names are not definitions of go2spec_combine.
func rejectAggregateUpdate(path string) error {
	// Metadata-only callers have no backing spec to inspect. Preserve that
	// existing API use; all file-writing entry points still require a file.
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Check the entire file, including definitions below Source0 and beyond a
	// long line that the legacy field scanner cannot handle.
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "%global" && fields[0] != "%define") {
			continue
		}
		name, _, _ := strings.Cut(fields[1], "(") // also reject parameterized definitions
		if name == "go2spec_combine" {
			return fmt.Errorf("%s: %w", path, errAggregateUpdateUnsupported)
		}
	}
	return nil
}
