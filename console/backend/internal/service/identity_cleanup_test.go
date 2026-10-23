package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/stretchr/testify/require"
)

func TestPurgeRemovesConsoleIdentityAfterProviderSuccess(t *testing.T) {
	exec := orchestrator.NewFakeExecutor()
	zones, svc, infra, cleanup := setupPurgeService(t, exec, nil)
	t.Cleanup(cleanup)
	q := suspendedQube(t, zones, svc, infra)
	r := newIssuerRig(t)
	WithCertIssuer(r.issuer)(svc.(*QubeServiceImpl))
	require.NoError(t, r.issuer.IssueFor(context.Background(), q))
	path := r.issuer.IdentityPath(q.Name)
	require.FileExists(t, path)
	require.NoError(t, svc.Purge(context.Background(), q.ID, q.Name))
	require.NoFileExists(t, path, "successful purge must not leave the bootstrap delivery document behind")
}

func TestIdentityCleanupOwnershipAndRestart(t *testing.T) {
	r := newIssuerRig(t)
	q := testQube("alpha")
	ctx := context.Background()
	require.NoError(t, r.issuer.IssueFor(ctx, q))
	doc := r.deliveredDoc(t, q.Name)
	shared, err := WriteSharedAgentUserData(r.dir, q.Name, doc)
	require.NoError(t, err)
	old, err := WriteSharedAgentUserData(r.dir, q.Name, doc+"# older version\n")
	require.NoError(t, err)
	// This local filename also matches alpha's content-addressed glob.
	other := testQube("alpha-012345abcdef")
	require.NoError(t, r.issuer.IssueFor(ctx, other))
	otherPath := r.issuer.IdentityPath(other.Name)
	// Cleanup must discover ownership from disk after a process restart.
	fresh := NewCertIssuer(nil, nil, r.dir, "", AgentPackage{})
	require.NoError(t, fresh.RemoveIdentityFor(ctx, q.ID, q.Name))
	require.NoFileExists(t, filepath.Join(r.dir, shared))
	require.NoFileExists(t, filepath.Join(r.dir, old))
	require.NoFileExists(t, r.issuer.IdentityPath(q.Name))
	require.FileExists(t, otherPath)
	require.NoError(t, fresh.RemoveIdentityFor(ctx, q.ID, q.Name))
}

func TestSharedRewritePreservesHexNamedNeighbor(t *testing.T) {
	dir := t.TempDir()
	neighbor := "alpha-012345abcdef"
	otherDoc := markIdentityOwner("other-id", neighbor, "#cloud-config\n")
	other, err := WriteAgentUserData(dir, neighbor, otherDoc)
	require.NoError(t, err)
	doc := markIdentityOwner("alpha-id", "alpha", "#cloud-config\n")
	mine, err := WriteSharedAgentUserData(dir, "alpha", doc)
	require.NoError(t, err)
	require.FileExists(t, other, "a content hash shaped suffix is not proof of ownership")
	found, err := FindSharedAgentUserData(dir, "alpha")
	require.NoError(t, err)
	require.Equal(t, mine, found)
}

func TestIdentityCleanupRefusesUnprovenFilesBeforeDeletingAny(t *testing.T) {
	for _, scenario := range []string{"unmarked", "foreign ID", "bad marker", "wrong content hash", "symlink", "directory", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			r := newIssuerRig(t)
			q := testQube("alpha")
			ctx := context.Background()
			require.NoError(t, r.issuer.IssueFor(ctx, q))
			kept := r.issuer.IdentityPath(q.Name)
			bad := filepath.Join(r.dir, "qubes-air-alpha-012345abcdef.yaml")
			doc := r.deliveredDoc(t, q.Name)
			switch scenario {
			case "unmarked":
				doc = "#cloud-config\nhostname: alpha\n"
			case "foreign ID":
				doc = markIdentityOwner("another-id", q.Name, "#cloud-config\n")
				bad = filepath.Join(r.dir, ContentAddressedSnippetName(q.Name, doc))
			case "bad marker":
				doc = "#cloud-config\n" + identityOwnerPrefix + "../unsafe alpha\n"
			case "symlink":
				require.NoError(t, os.Symlink(kept, bad))
			case "directory":
				require.NoError(t, os.Mkdir(bad, 0700))
			case "oversized":
				doc = strings.Repeat("x", maxIdentityBytes+1)
			}
			if scenario != "symlink" && scenario != "directory" {
				require.NoError(t, os.WriteFile(bad, []byte(doc), 0600))
			}
			require.Error(t, r.issuer.RemoveIdentityFor(ctx, q.ID, q.Name))
			require.FileExists(t, kept, "validate all candidates before deleting anything")
			_, err := os.Lstat(bad)
			require.NoError(t, err)
		})
	}
}

func TestIdentityCleanupCancellationAndChangedFile(t *testing.T) {
	r := newIssuerRig(t)
	q := testQube("alpha")
	require.NoError(t, r.issuer.IssueFor(context.Background(), q))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, r.issuer.RemoveIdentityFor(ctx, q.ID, q.Name), context.Canceled)
	root, err := os.OpenRoot(r.dir)
	require.NoError(t, err)
	defer root.Close()
	files, err := collectOwnedIdentities(context.Background(), root, q.ID, q.Name)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.NoError(t, os.WriteFile(r.issuer.IdentityPath(q.Name), []byte("changed"), 0600))
	require.ErrorContains(t, removeOwnedIdentity(context.Background(), root, files[0]), "changed")
	require.FileExists(t, r.issuer.IdentityPath(q.Name))
	for _, name := range []string{"../outside", "", "alpha/child"} {
		require.Error(t, r.issuer.RemoveIdentityFor(context.Background(), q.ID, name))
	}
	require.Error(t, r.issuer.RemoveIdentityFor(context.Background(), "bad\nID", q.Name))
}

func TestPurgeIdentityCleanupFailureIsRetryable(t *testing.T) {
	exec := orchestrator.NewFakeExecutor()
	zones, svc, infra, cleanup := setupPurgeService(t, exec, nil)
	t.Cleanup(cleanup)
	q := suspendedQube(t, zones, svc, infra)
	r := newIssuerRig(t)
	WithCertIssuer(r.issuer)(svc.(*QubeServiceImpl))
	ctx := context.Background()
	require.NoError(t, r.issuer.IssueFor(ctx, q))
	path := r.issuer.IdentityPath(q.Name)
	exec.FailOn[orchestrator.ActionDestroy] = errors.New("provider still owns disk")
	require.Error(t, svc.Purge(ctx, q.ID, q.Name))
	require.FileExists(t, path)
	delete(exec.FailOn, orchestrator.ActionDestroy)
	// A broken ownership marker cannot produce a successful purge.
	require.NoError(t, os.WriteFile(path, []byte("unmarked"), 0600))
	require.Error(t, svc.Purge(ctx, q.ID, q.Name))
	got, err := svc.GetByID(ctx, q.ID)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusError, got.Status)
	require.FileExists(t, path)
	// Simulate an operator reconciling the unknown file, then retry normally.
	require.NoError(t, os.Remove(path))
	require.NoError(t, svc.Purge(ctx, q.ID, q.Name))
	got, err = svc.GetByID(ctx, q.ID)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusPurged, got.Status)
}

func TestIdentityCleanupHonorsDeadlineWhileIssuanceHoldsLock(t *testing.T) {
	r := newIssuerRig(t)
	q := testQube("alpha")
	require.NoError(t, r.issuer.IssueFor(context.Background(), q))
	require.NoError(t, r.issuer.lockIdentity(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.issuer.RemoveIdentityFor(ctx, q.ID, q.Name) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		r.issuer.unlockIdentity()
		<-done
		t.Fatal("cleanup did not honor its deadline while waiting for issuance")
	}
	r.issuer.unlockIdentity()
	require.FileExists(t, r.issuer.IdentityPath(q.Name))
	require.NoError(t, r.issuer.RemoveIdentityFor(context.Background(), q.ID, q.Name))
}
