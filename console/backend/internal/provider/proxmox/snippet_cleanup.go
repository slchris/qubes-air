package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/slchris/qubes-air/console/internal/provider"
)

// cleanupIdentity removes only the precisely recorded snippet, never a directory
// or a volume selected by a glob. The name and volume are checkpointed before upload.
func (a *Adapter) cleanupIdentity(ctx context.Context, in provider.Infra) error {
	if in.IdentityVol == "" {
		return nil
	}
	store, name, ok := strings.Cut(in.IdentityVol, ":snippets/")
	if !ok || store == "" || !snippetNameRE.MatchString(name) || strings.Contains(name, "/") {
		return errors.New("proxmox: invalid recorded snippet identity")
	}
	present, err := a.volumePresent(ctx, in.Node, in.IdentityVol)
	if err != nil || !present {
		return err
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/storage/%s/content/%s", url.PathEscape(in.Node), url.PathEscape(store), url.PathEscape(in.IdentityVol))
	var task string
	if err := a.client.delete(ctx, path, &task); err != nil {
		return err
	}
	if task != "" {
		return a.client.WaitTask(ctx, in.Node, task, a.opts.TaskPoll)
	}
	return nil
}
