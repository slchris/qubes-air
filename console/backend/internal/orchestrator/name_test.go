package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidQubeName(t *testing.T) {
	valid := []string{
		"dev-work", "web01", "a", "gpu_node.1", "A-B_c.9",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		if !ValidQubeName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	// These are the security-critical rejections: anything that could inject a
	// shell metacharacter, a provider flag, an address separator, or whitespace.
	invalid := []string{
		"",                      // empty
		"a b",                   // space
		"a;rm -rf /",            // command separator
		"$(whoami)",             // command substitution
		"`id`",                  // backtick substitution
		"a&&b",                  // logical operator
		"a|b",                   // pipe
		"a>b",                   // redirect
		"-target=evil",          // leading dash -> looks like a flag
		"--var=x",               // flag
		"a\"b",                  // quote
		"a'b",                   // quote
		"a b\nc",                // newline
		"a\tb",                  // tab
		"a/b",                   // path separator
		"a\\b",                  // backslash
		"a[0]",                  // brackets (address syntax)
		"a{b}",                  // braces
		"a\x00b",                // null byte
		"名字",                    // non-ascii
		strings.Repeat("a", 65), // too long
	}
	for _, name := range invalid {
		if ValidQubeName(name) {
			t.Errorf("expected %q to be REJECTED", name)
		}
	}
}

// TestNoopExecutorRejectsMaliciousName ensures even the DB-only executor
// rejects unsafe names consistently with the provider executor, so no code path
// accepts a name another one refuses.
func TestNoopExecutorRejectsMaliciousName(t *testing.T) {
	exec := NewNoopExecutor()
	for _, name := range []string{"a;rm -rf /", "$(whoami)", "a b", "-target=x", "web[0]", ""} {
		err := exec.Provision(context.Background(), name)
		var invalid *ErrInvalidQubeName
		if !errors.As(err, &invalid) {
			t.Errorf("Provision(%q): expected ErrInvalidQubeName, got %v", name, err)
		}
	}
}
