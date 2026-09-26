package service

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})
	return &buf
}

// The bootstrap token is a secret (AGENTS.md §5), so minting and delivering it
// must leave no copy in the console's log — only where the document went.
// Checked for both delivery paths, because each logs a different destination.
func TestIssueForNeverLogsTheBootstrapToken(t *testing.T) {
	for _, tc := range []struct {
		name      string
		datastore string
	}{
		{"ssh upload staging", ""},
		{"shared snippet storage", "cephfs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newIssuerRig(t)
			r.issuer.WithSnippetDatastore(tc.datastore)
			logged := captureLog(t)

			require.NoError(t, r.issuer.IssueFor(context.Background(), testQube("dev-work")))

			file := SnippetFileName("dev-work")
			if tc.datastore != "" {
				var err error
				file, err = FindSharedAgentUserData(r.dir, "dev-work")
				require.NoError(t, err)
			}
			doc, err := os.ReadFile(filepath.Join(r.dir, file))
			require.NoError(t, err)
			token := tokenFromDoc(t, string(doc))
			require.NotEmpty(t, token)

			assert.Contains(t, logged.String(), "wrote agent identity", "the delivery itself should still be visible")
			assert.NotContains(t, logged.String(), token, "the bootstrap token reached the console log")
		})
	}
}

// Whatever copy of the delivered document survives — on the node, on a share,
// in the cloud-init drive — carries a token the console honors once and only
// for an hour. That bound is what the snippet's residual exposure rests on, and
// docs/production-readiness-gaps.md (G-D7) and docs/deployment-requirements.md
// quote it, so it is pinned here end to end, from IssueFor to Redeem, as an
// absolute hour rather than relative to the constant: raising the default must
// fail this test and send whoever does it to those documents.
func TestDeliveredTokenIsSingleUseAndShortLived(t *testing.T) {
	const documentedBound = time.Hour
	require.LessOrEqual(t, defaultBootstrapTokenTTL, documentedBound,
		"the default bootstrap token TTL grew past what the G-D7 documentation promises")

	r := newIssuerRig(t)
	ctx := context.Background()
	issued := time.Now()
	require.NoError(t, r.issuer.IssueFor(ctx, testQube("dev-work")))
	token := tokenFromDoc(t, r.deliveredDoc(t, "dev-work"))

	_, err := r.tokens.Redeem(ctx, token, issued.Add(documentedBound+time.Minute))
	require.ErrorIs(t, err, repository.ErrBootstrapTokenRejected, "the delivered token outlived the documented hour")

	_, err = r.tokens.Redeem(ctx, token, time.Now())
	require.NoError(t, err, "an expired-time probe must not have consumed the token")

	_, err = r.tokens.Redeem(ctx, token, time.Now())
	require.ErrorIs(t, err, repository.ErrBootstrapTokenRejected, "a delivered token redeemed twice")
}
