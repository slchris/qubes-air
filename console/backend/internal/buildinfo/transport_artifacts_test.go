package buildinfo_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

var transportHelpers = []string{"relay-call", "relay-bootstrap", "issue-relay-cert", "list-endpoints"}

const transportStampPackage = "github.com/slchris/qubes-air/console/internal/buildinfo"

func transportRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runTransportBuild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
}

func transportVersion(t *testing.T, binary string) string {
	t.Helper()
	state := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Dir = state
	cmd.Env = append(os.Environ(), "PATH="+state, "QUBES_AIR_ENCRYPTION_KEY=")
	// Keep stdin open without sending data: --version must not read an RPC body.
	cmd.Stdin = reader
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("--version: %v, stderr=%q", err, stderr.String())
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("version query created state: entries=%v, error=%v", entries, err)
	}
	return string(out)
}

func TestTransportHelpersReportBuildIdentity(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	const built = "2026-10-24T00:00:00Z"
	for _, version := range []string{"", "v1.2.3-dirty"} {
		t.Run("version="+version, func(t *testing.T) {
			binDir := t.TempDir()
			args := []string{"build", "-o", binDir + string(os.PathSeparator)}
			want := "version=unknown revision=unknown build_time=unknown tree=unknown\n"
			if version != "" {
				args = append(args, "-ldflags", "-X "+transportStampPackage+".version="+version+
					" -X "+transportStampPackage+".revision="+revision+" -X "+transportStampPackage+".buildTime="+built)
				want = "version=" + version + " revision=" + revision + " build_time=" + built + " tree=dirty\n"
			}
			for _, name := range transportHelpers {
				args = append(args, "./cmd/"+name)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", args...)
			cmd.Dir = filepath.Join(transportRepoRoot(t), "console", "backend")
			runTransportBuild(t, cmd)
			for _, name := range transportHelpers {
				t.Run(name, func(t *testing.T) {
					if got := transportVersion(t, filepath.Join(binDir, name)); got != name+" "+want {
						t.Fatalf("identity = %q, want %q", got, name+" "+want)
					}
				})
			}
		})
	}
}

func TestReleaseScriptBuildsTransportHelpers(t *testing.T) {
	root := transportRepoRoot(t)
	revision, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range transportHelpers {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), name)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "build-release-binary.sh"), "./cmd/"+name, binary)
			cmd.Env = append(os.Environ(), "VERSION=v1.2.3")
			runTransportBuild(t, cmd)
			line := transportVersion(t, binary)
			prefix := name + " version=v1.2.3 revision=" + strings.TrimSpace(string(revision)) + " build_time="
			if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " tree=clean\n") {
				t.Fatalf("release identity = %q", line)
			}
			stamp := strings.TrimSuffix(strings.TrimPrefix(line, prefix), " tree=clean\n")
			if _, err := time.Parse(time.RFC3339, stamp); err != nil {
				t.Fatalf("invalid build time: %v", err)
			}
		})
	}
}

// A build alone cannot prove a helper is downloadable or covered by the
// manifest: require both handoff lists in the actual publishing workflow.
func TestTransportHelpersArePublishedWithChecksums(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				With struct {
					Files string `yaml:"files"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	b, err := os.ReadFile(filepath.Join(transportRepoRoot(t), ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &workflow); err != nil {
		t.Fatal(err)
	}
	checksummed, published := make(map[string]bool), make(map[string]bool)
	for _, step := range workflow.Jobs["release"].Steps {
		if step.Name == "Checksums" {
			for _, word := range strings.Fields(step.Run) {
				checksummed[word] = true
			}
		}
		for _, name := range strings.Fields(step.With.Files) {
			published[name] = true
		}
	}
	for _, name := range transportHelpers {
		if !checksummed[name] || !published["dist/"+name] {
			t.Errorf("%s missing from published assets or checksum inputs", name)
		}
	}
}
