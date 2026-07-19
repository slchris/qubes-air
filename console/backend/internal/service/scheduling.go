package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/scheduler"
)

// SecretReader reads a decrypted secret from the credential store.
//
// Narrowed to the one method scheduling needs so the service cannot reach the
// rest of the credential repository, and so tests can supply a stub without a
// database.
type SecretReader interface {
	GetSecret(ctx context.Context, id string) (string, error)
}

// NewZoneCredentialResolver returns the resolver that turns a zone into live
// cluster credentials by decrypting the secret it references.
//
// A zone stores only a credential ID. The secret is fetched here, used for one
// call, and never persisted back into the zone — ZoneConfig is returned by the
// zones API in cleartext, so a secret placed there would be handed to every
// caller that can list zones.
func NewZoneCredentialResolver(zoneRepo repository.ZoneRepository, secrets SecretReader) scheduler.CredentialResolver {
	return func(ctx context.Context, zoneID string) (scheduler.Credentials, error) {
		zone, err := zoneRepo.GetByID(ctx, zoneID)
		if err != nil {
			return scheduler.Credentials{}, fmt.Errorf("load zone %q: %w", zoneID, err)
		}
		pc := zone.Config.Proxmox
		if pc == nil {
			return scheduler.Credentials{}, fmt.Errorf("zone %q has no proxmox config", zone.Name)
		}
		if pc.CredentialID == "" {
			return scheduler.Credentials{}, fmt.Errorf(
				"zone %q has no credential_id; add a Proxmox credential and reference it from the zone", zone.Name)
		}

		secret, err := secrets.GetSecret(ctx, pc.CredentialID)
		if err != nil {
			return scheduler.Credentials{}, fmt.Errorf("zone %q: read credential: %w", zone.Name, err)
		}

		creds := parseProxmoxSecret(secret)
		creds.Endpoint = zone.Config.Endpoint
		if !creds.Valid() {
			return scheduler.Credentials{}, fmt.Errorf(
				"zone %q: credential is not a usable Proxmox secret (want an API token \"user@realm!id=secret\" or \"user@realm:password\")", zone.Name)
		}
		return creds, nil
	}
}

// parseProxmoxSecret interprets a stored secret.
//
// Two shapes are accepted, distinguished by the '!' that only ever appears in a
// token id:
//
//	user@realm!tokenid=secret   -> API token (preferred: scoped and revocable)
//	user@realm:password         -> username and password
func parseProxmoxSecret(secret string) scheduler.Credentials {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return scheduler.Credentials{}
	}
	if strings.Contains(secret, "!") && strings.Contains(secret, "=") {
		return scheduler.Credentials{APIToken: secret}
	}
	if user, pass, ok := strings.Cut(secret, ":"); ok {
		return scheduler.Credentials{Username: user, Password: pass}
	}
	return scheduler.Credentials{}
}

// NodeInfo is a node's capacity as reported to the UI, plus whether the
// scheduler would currently consider it.
type NodeInfo struct {
	Name          string  `json:"name"`
	Online        bool    `json:"online"`
	MaxCPU        int     `json:"max_cpu"`
	CPUUsage      float64 `json:"cpu_usage"`
	MemUsedBytes  int64   `json:"mem_used_bytes"`
	MemTotalBytes int64   `json:"mem_total_bytes"`
	MemFreeBytes  int64   `json:"mem_free_bytes"`
}

// CapacityReader exposes cluster capacity for display.
type CapacityReader interface {
	Nodes(ctx context.Context, zoneID string) ([]NodeInfo, error)
}

// Nodes returns live per-node capacity for a zone.
//
// This exists so the UI can show what "automatic" is choosing between. Offering
// a node picker without capacity numbers asks the operator to guess, which is
// how a cluster ends up with everything piled onto whichever node was
// hardcoded first.
func (c *ClusterScheduler) Nodes(ctx context.Context, zoneID string) ([]NodeInfo, error) {
	creds, err := c.resolve(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	nodes, err := scheduler.NewProxmoxProvider(creds).Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster capacity: %w", err)
	}
	out := make([]NodeInfo, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeInfo{
			Name: n.Name, Online: n.Online, MaxCPU: n.MaxCPU, CPUUsage: n.CPUUsage,
			MemUsedBytes: n.MemUsedBytes, MemTotalBytes: n.MemTotalBytes,
			MemFreeBytes: n.FreeMemBytes(),
		})
	}
	return out, nil
}

// PlacementDecider chooses the node a qube should run on.
type PlacementDecider interface {
	Place(ctx context.Context, zoneID string, req scheduler.Requirements) (*scheduler.Placement, error)
}

// ClusterScheduler resolves credentials, reads live capacity and picks a node.
type ClusterScheduler struct {
	resolve scheduler.CredentialResolver
	sched   *scheduler.Scheduler
}

// NewClusterScheduler wires a scheduler onto a credential resolver.
func NewClusterScheduler(resolve scheduler.CredentialResolver) *ClusterScheduler {
	return &ClusterScheduler{resolve: resolve, sched: scheduler.New()}
}

// Place selects a node for a qube in the given zone.
//
// Capacity is read fresh on every call rather than cached: a stale view is how
// a scheduler piles several guests onto the node it last saw as empty.
func (c *ClusterScheduler) Place(ctx context.Context, zoneID string, req scheduler.Requirements) (*scheduler.Placement, error) {
	creds, err := c.resolve(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	nodes, err := scheduler.NewProxmoxProvider(creds).Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cluster capacity: %w", err)
	}
	return c.sched.Select(ctx, nodes, req)
}

// resolvePlacement decides where a qube runs, honouring an explicit pin.
//
// Precedence is: the qube's own node, then the scheduler, then the zone
// default. An explicit pin always wins — automatic placement is a convenience,
// not a policy that overrides what an operator asked for.
func (s *QubeServiceImpl) resolvePlacement(ctx context.Context, qube *models.Qube, zone *models.Zone) (string, string, error) {
	if qube.Spec.Node != "" {
		return qube.Spec.Node, "pinned by request", nil
	}
	if s.placer != nil {
		placement, err := s.placer.Place(ctx, zone.ID, scheduler.Requirements{
			MemoryMB: qube.Spec.Memory,
			VCPU:     qube.Spec.VCPU,
		})
		if err == nil && placement.Node != "" {
			return placement.Node, placement.Reason, nil
		}
		// A cluster with genuinely no room must fail rather than silently fall
		// back to a default node that cannot fit the qube either.
		if err != nil && isCapacityError(err) {
			return "", "", err
		}
		// Anything else (unreachable cluster, missing credential) degrades to the
		// zone default: scheduling is an optimisation, not a prerequisite.
	}
	if zone.Config.Proxmox != nil && zone.Config.Proxmox.Node != "" {
		return zone.Config.Proxmox.Node, "zone default", nil
	}

	// Nothing could be decided — typically a zone that has not been configured
	// for provisioning yet. Leave the node unset rather than refusing to record
	// the qube: the tfvars renderer already fails loudly, by name, if a qube
	// reaches provisioning without a node, so this is deferred rather than
	// skipped. Only a cluster that genuinely has no room (above) is fatal here.
	return "", "no node could be resolved; will be rejected at provision time unless the zone is configured", nil
}

// isCapacityError reports whether the cluster answered but had no room.
func isCapacityError(err error) bool {
	return err != nil && strings.Contains(err.Error(), scheduler.ErrInsufficientCapacity.Error())
}
