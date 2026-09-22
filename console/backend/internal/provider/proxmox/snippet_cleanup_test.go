package proxmox

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestSnippetCleanupIsRecordedAndIdempotent(t *testing.T) {
	deleted := false
	deletes := 0
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access/permissions"):
			writeData(w, map[string]map[string]int{r.URL.Query().Get("path"): {"Datastore.Allocate": 1}})
		case r.Method == http.MethodDelete:
			require.True(t, strings.HasSuffix(r.URL.Path, "/content/local:snippets/agent.yaml"))
			deleted = true
			deletes++
			writeData(w, testUPID)
		case strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		default:
			items := []map[string]string{}
			if !deleted {
				items = append(items, map[string]string{"volid": "local:snippets/agent.yaml"})
			}
			writeData(w, items)
		}
	}, Options{})
	in := provider.Infra{Node: "node1", IdentityVol: "local:snippets/agent.yaml"}
	for range 2 {
		require.NoError(t, ad.DestroyStorage(context.Background(), testQube(), in))
		require.NoError(t, ad.VerifyDestroyed(context.Background(), testQube(), in))
	}
	require.Equal(t, 1, deletes)
	in.IdentityVol = "local:snippets/../../target.yaml"
	require.Error(t, ad.DestroyStorage(context.Background(), testQube(), in))
}
