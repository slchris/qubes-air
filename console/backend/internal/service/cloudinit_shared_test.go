package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestSharedWriteIsFoundByVolumeLookup(t *testing.T) {
	dir := t.TempDir()
	name, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)

	found, err := FindSharedAgentUserData(dir, "dev-work")
	require.NoError(t, err)
	assert.Equal(t, name, found, "the file that was written is not the one a later lookup finds")

	// 0644, not 0600: the PVE nodes read this as a different user entirely.
	info, err := os.Stat(filepath.Join(dir, name))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(),
		"a mode the hypervisor cannot read makes the qube boot with no identity")
}

// Re-rendering must leave exactly one file, or the share grows forever and the
// lookup that keeps provisioning honest starts refusing.
func TestRewriteSupersedesTheOldVersion(t *testing.T) {
	dir := t.TempDir()
	first, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)
	second, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 2\n")
	require.NoError(t, err)

	require.NotEqual(t, first, second, "new content must produce a new name")
	assert.Equal(t, []string{second}, namesIn(t, dir), "the superseded identity was left behind")
}

// Writing the same content twice is a no-op, not a rebuild. If it produced a
// new name, every provision would ForceNew the compute VM.
func TestRewritingIdenticalContentIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)
	second, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, []string{first}, namesIn(t, dir))
}

// Reaping must never reach another qube's files. Deleting one belonging to a
// running VM is unrecoverable from the console's side.
func TestReapingIsScopedToOneQube(t *testing.T) {
	dir := t.TempDir()
	other, err := WriteSharedAgentUserData(dir, "dev-work-2", "#cloud-config\nother\n")
	require.NoError(t, err)
	legacy := SnippetFileName("dev-work-2")
	require.NoError(t, os.WriteFile(filepath.Join(dir, legacy), []byte("legacy"), 0o644))

	mine, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nmine\n")
	require.NoError(t, err)
	_, err = WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nmine v2\n")
	require.NoError(t, err)

	got := namesIn(t, dir)
	assert.Contains(t, got, other, "another qube's current identity was reaped")
	assert.Contains(t, got, legacy, "another qube's legacy identity was reaped")
	assert.NotContains(t, got, mine, "this qube's superseded identity was not reaped")
}

// A prefix-sharing name must not be swept by its neighbor. "dev" and
// "dev-work" both start with "qubes-air-dev".
func TestPrefixSharingQubesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	long, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nlong\n")
	require.NoError(t, err)
	short, err := WriteSharedAgentUserData(dir, "dev", "#cloud-config\nshort\n")
	require.NoError(t, err)

	// Re-render the short one; the long one must survive untouched.
	_, err = WriteSharedAgentUserData(dir, "dev", "#cloud-config\nshort v2\n")
	require.NoError(t, err)

	got := namesIn(t, dir)
	assert.Contains(t, got, long, "re-rendering \"dev\" reaped \"dev-work\"'s identity")
	assert.NotContains(t, got, short)

	found, err := FindSharedAgentUserData(dir, "dev-work")
	require.NoError(t, err)
	assert.Equal(t, long, found, "lookup for \"dev-work\" was confused by \"dev\"")
}

// Two identities for one qube means reaping failed and there is no evidence
// here about which one the VM booted with. Picking would be a coin flip that
// can rebuild a healthy qube with an identity it never had.
func TestAmbiguousIdentityIsRefusedNotGuessed(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)
	// Plant a second version behind the reaper's back.
	stray := ContentAddressedSnippetName("dev-work", "#cloud-config\nfoo: 2\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, stray), []byte("x"), 0o644))

	_, err = FindSharedAgentUserData(dir, "dev-work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 identity snippets")
}

func TestLookupOfAnUnknownQubeIsEmptyNotAnError(t *testing.T) {
	dir := t.TempDir()
	name, err := FindSharedAgentUserData(dir, "never-existed")
	require.NoError(t, err, "a qube with no identity yet is a normal state, not a fault")
	assert.Empty(t, name)

	name, err = FindSharedAgentUserData(filepath.Join(dir, "not-mounted-yet"), "q")
	require.NoError(t, err)
	assert.Empty(t, name)
}

// Purge must remove every version, because the caller no longer knows the
// current name once the qube's config is gone.
func TestRemoveSweepsEveryVersion(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteSharedAgentUserData(dir, "dev-work", "#cloud-config\nfoo: 1\n")
	require.NoError(t, err)
	stray := ContentAddressedSnippetName("dev-work", "#cloud-config\nfoo: 2\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, stray), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SnippetFileName("dev-work")), []byte("y"), 0o644))
	keep, err := WriteSharedAgentUserData(dir, "other", "#cloud-config\nother\n")
	require.NoError(t, err)

	require.NoError(t, RemoveAgentUserData(dir, "dev-work"))
	assert.Equal(t, []string{keep}, namesIn(t, dir))
}
