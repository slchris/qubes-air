package service

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func renderFixture(t *testing.T) (string, *pki.Bundle) {
	t.Helper()
	ca, err := pki.NewCA("test-ca", 0)
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("agent-dev-work", 0)
	require.NoError(t, err)
	out, err := RenderAgentUserData("remote-dev", bundle, "0.0.0.0:8443")
	require.NoError(t, err)
	return out, bundle
}

type cloudConfig struct {
	WriteFiles []struct {
		Path        string `yaml:"path"`
		Permissions string `yaml:"permissions"`
		Owner       string `yaml:"owner"`
		Content     string `yaml:"content"`
	} `yaml:"write_files"`
	Runcmd []any `yaml:"runcmd"`
}

func parseConfig(t *testing.T, out string) cloudConfig {
	t.Helper()
	var cc cloudConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &cc), "rendered user-data must be valid YAML")
	return cc
}

// TestRenderedUserDataIsValidYAML — cloud-init silently ignores a document it
// cannot parse, so a malformed render produces a VM that boots fine and simply
// has no agent identity. That failure is invisible until something tries to
// connect.
func TestRenderedUserDataIsValidYAML(t *testing.T) {
	out, _ := renderFixture(t)
	assert.True(t, strings.HasPrefix(out, "#cloud-config\n"),
		"cloud-init requires the #cloud-config header to treat this as config")

	cc := parseConfig(t, out)
	assert.Len(t, cc.WriteFiles, 4)
	assert.NotEmpty(t, cc.Runcmd)
}

// TestPrivateKeySurvivesYAMLRoundTrip is the one that would fail silently.
// PEM is multi-line; if the literal block mangles it — an indentation slip, a
// trailing-space rule — the file still looks like a key and openssl rejects it
// only on the remote, at boot, where nobody is watching.
func TestPrivateKeySurvivesYAMLRoundTrip(t *testing.T) {
	out, bundle := renderFixture(t)
	cc := parseConfig(t, out)

	var keyContent string
	for _, f := range cc.WriteFiles {
		if strings.HasSuffix(f.Path, "agent-key.pem") {
			keyContent = f.Content
		}
	}
	require.NotEmpty(t, keyContent, "the agent key must be delivered")

	assert.Equal(t, strings.TrimRight(bundle.KeyPEM, "\n"), strings.TrimRight(keyContent, "\n"),
		"the key must arrive byte-identical")

	// Prove it, rather than trusting string equality: parse the extracted key.
	cmd := exec.Command("openssl", "ec", "-noout", "-text")
	cmd.Stdin = strings.NewReader(keyContent)
	if err := cmd.Run(); err != nil {
		t.Errorf("openssl could not parse the delivered key: %v", err)
	}
}

// TestPrivateKeyIsNotWorldReadable — a world-readable key on a multi-user host
// hands the agent's identity to every local account, turning a revocable
// credential back into an ambient one.
func TestPrivateKeyIsNotWorldReadable(t *testing.T) {
	out, _ := renderFixture(t)
	cc := parseConfig(t, out)

	for _, f := range cc.WriteFiles {
		if strings.HasSuffix(f.Path, "agent-key.pem") {
			assert.Equal(t, "0600", f.Permissions, "the private key must not be readable by other users")
		}
		assert.Equal(t, "root:root", f.Owner)
	}
}

// TestRemoteNameReachesTheAgent — qubesair.Ping returns this value, and
// CheckReachable compares against it.
func TestRemoteNameReachesTheAgent(t *testing.T) {
	out, _ := renderFixture(t)
	cc := parseConfig(t, out)

	var env string
	for _, f := range cc.WriteFiles {
		if strings.HasSuffix(f.Path, "agent.env") {
			env = f.Content
		}
	}
	assert.Contains(t, env, "QUBESAIR_REMOTE_NAME=remote-dev")
	assert.Contains(t, env, "QUBESAIR_LISTEN=0.0.0.0:8443")
}

// TestCAPrivateKeyNeverDelivered — the bundle excludes it, and the renderer
// must not reintroduce it. This document goes to a host assumed compromisable.
func TestCAPrivateKeyNeverDelivered(t *testing.T) {
	out, _ := renderFixture(t)
	assert.Equal(t, 1, strings.Count(out, "BEGIN EC PRIVATE KEY"),
		"exactly one private key — the agent's — may appear")
}

// TestRenderRejectsIncompleteInput — failing here is far better than emitting a
// document that installs nothing and looks like it worked.
func TestRenderRejectsIncompleteInput(t *testing.T) {
	ca, err := pki.NewCA("t", 0)
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("a", 0)
	require.NoError(t, err)

	_, err = RenderAgentUserData("remote", nil, "")
	assert.Error(t, err, "a missing bundle must fail")

	_, err = RenderAgentUserData("", bundle, "")
	assert.Error(t, err, "a missing remote name must fail: qubesair.Ping reports it")
}

// TestSnippetVolumeIDFormat — Proxmox resolves a snippet by this exact shape,
// and the datastore must declare the snippets content type or the volume is
// not found even with the file present.
func TestSnippetVolumeIDFormat(t *testing.T) {
	assert.Equal(t, "local:snippets/qubes-air-dev-work.yaml", SnippetVolumeID("local", "dev-work"))
	assert.Equal(t, "local:snippets/qubes-air-x.yaml", SnippetVolumeID("", "x"),
		"an unset datastore falls back to local")
	assert.Equal(t, "qubes-air-dev-work.yaml", SnippetFileName("dev-work"),
		"the on-disk name must match the volume id")
}
