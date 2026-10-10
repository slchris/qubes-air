package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The qube name is the only variable part of a snippet path, so this allowlist
// is what keeps a name from becoming a path. It is deliberately narrower than
// orchestrator.ValidQubeName: '.' is rejected because, once the name is joined
// to a directory, it is the one remaining character that can form a traversal
// segment.
func TestValidateQubeName(t *testing.T) {
	valid := []string{
		"a",
		"dev-work",
		"web01",
		"gpu_node1",
		"A-B_c9",
		strings.Repeat("a", maxQubeNameLen),
	}
	for _, name := range valid {
		assert.NoError(t, validateQubeName(name), "expected %q to be valid", name)
	}

	// The security-critical rejections: anything that can escape the identity
	// directory or smuggle a separator, a dot segment, or a shell metacharacter.
	invalid := []string{
		"",                                    // empty
		"..",                                  // a bare parent segment
		"../evil",                             // traversal
		"x/../../evil",                        // traversal that would land outside dir
		"a/b",                                 // forward separator
		"a\\b",                                // backslash
		"a.b",                                 // dot (narrower than orchestrator.ValidQubeName)
		"a b",                                 // space
		"a;rm -rf /",                          // command separator
		"$(id)",                               // command substitution
		"a\x00b",                              // null byte
		"名字",                                  // non-ascii
		strings.Repeat("a", maxQubeNameLen+1), // too long
	}
	for _, name := range invalid {
		assert.Error(t, validateQubeName(name), "expected %q to be REJECTED", name)
	}
}

// A rejected name must fail before any path is built or any file is touched:
// the whole point of the check is that the malformed name never reaches a
// rename. The identity directory is deliberately left absent, so its continued
// absence proves neither writer even attempted the mkdir that precedes a write.
func TestSnippetWritersRejectUnsafeQubeNames(t *testing.T) {
	unsafe := []string{
		"",
		"..",
		"x/../../evil",
		"a/b",
		"a\\b",
		"a.b",
		"a b",
		"a;rm -rf /",
		"$(id)",
		"a\x00b",
		"名字",
		strings.Repeat("a", maxQubeNameLen+1),
	}
	writers := map[string]func(dir, qubeName, userData string) (string, error){
		"WriteAgentUserData":       WriteAgentUserData,
		"WriteSharedAgentUserData": WriteSharedAgentUserData,
	}
	for writer, write := range writers {
		t.Run(writer, func(t *testing.T) {
			for _, qube := range unsafe {
				root := t.TempDir()
				dir := filepath.Join(root, "identity") // never created

				got, err := write(dir, qube, "#cloud-config\n")
				require.Error(t, err, "qube name %q must be rejected", qube)
				assert.Empty(t, got, "a rejected write must not return a path")

				_, statErr := os.Stat(dir)
				assert.True(t, os.IsNotExist(statErr),
					"rejected qube name %q still created the identity directory", qube)
				entries, readErr := os.ReadDir(root)
				require.NoError(t, readErr)
				assert.Empty(t, entries,
					"rejected qube name %q still touched the filesystem", qube)
			}
		})
	}
}

// Valid, already-conventional names must behave exactly as they did before the
// check: a file is written and the returned name identifies the qube.
func TestSnippetWritersAcceptConventionalNames(t *testing.T) {
	for _, qube := range []string{"dev-work", "web01", "gpu_node1", "A-B_c9", strings.Repeat("a", maxQubeNameLen)} {
		root := t.TempDir()
		dir := filepath.Join(root, "identity")

		path, err := WriteAgentUserData(dir, qube, "#cloud-config\n")
		require.NoError(t, err, "%q is a conventional name and must be accepted", qube)
		_, statErr := os.Stat(path)
		require.NoError(t, statErr, "the accepted identity file was not written")

		name, err := WriteSharedAgentUserData(dir, qube, "#cloud-config\n")
		require.NoError(t, err, "%q is a conventional name and must be accepted", qube)
		assert.True(t, strings.HasPrefix(name, "qubes-air-"+qube+"-"),
			"name %q does not identify its qube", name)
	}
}
