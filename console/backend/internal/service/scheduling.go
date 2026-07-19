package service

import (
	"context"
	"fmt"
	"log"
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

// CapacityKind discriminates how a provider expresses "can I fit another
// workload?".
//
// This is not cosmetic. The two kinds ask genuinely different questions, and
// conflating them produced a UI that offered a node picker for clouds where
// node selection does not exist as a concept.
type CapacityKind string

// Capacity kinds.
const (
	// CapacityKindNodePool is a finite pool of machines you already own, where
	// placement is bin-packing and the binding constraint is physical memory.
	// Proxmox and other on-premise hypervisors work this way.
	CapacityKindNodePool CapacityKind = "node_pool"

	// CapacityKindQuota is elastic capacity, where the provider decides which
	// physical machine runs a workload and you cannot see or influence it. The
	// binding constraints are the account's quota and its cost, not free RAM,
	// so "how much am I using" replaces "where does this fit".
	CapacityKindQuota CapacityKind = "quota"

	// CapacityKindUnknown means the provider cannot report capacity yet.
	CapacityKindUnknown CapacityKind = "unknown"
)

// QuotaInfo is elastic-provider usage against account limits.
//
// Limits are the hard constraint; cost is usually the one that actually binds
// first, which is why it is reported alongside rather than buried in billing.
type QuotaInfo struct {
	InstancesUsed  int     `json:"instances_used"`
	InstancesLimit int     `json:"instances_limit,omitempty"`
	VCPUUsed       int     `json:"vcpu_used"`
	VCPULimit      int     `json:"vcpu_limit,omitempty"`
	MemoryMBUsed   int     `json:"memory_mb_used"`
	MonthToDateUSD float64 `json:"month_to_date_usd,omitempty"`
	HourlyRateUSD  float64 `json:"hourly_rate_usd,omitempty"`
}

// ZoneCapacity is what a zone can tell the UI about its headroom.
//
// Exactly one of Nodes or Quota is populated, selected by Kind. Note carries an
// operator-facing explanation when a provider cannot answer.
type ZoneCapacity struct {
	Kind  CapacityKind `json:"kind"`
	Nodes []NodeInfo   `json:"nodes,omitempty"`
	Quota *QuotaInfo   `json:"quota,omitempty"`
	Note  string       `json:"note,omitempty"`
}

// NodeInfo is one machine in a node pool.
type NodeInfo struct {
	Name          string  `json:"name"`
	Online        bool    `json:"online"`
	MaxCPU        int     `json:"max_cpu"`
	CPUUsage      float64 `json:"cpu_usage"`
	MemUsedBytes  int64   `json:"mem_used_bytes"`
	MemTotalBytes int64   `json:"mem_total_bytes"`
	MemFreeBytes  int64   `json:"mem_free_bytes"`
}

// CapacityReader exposes a zone's capacity for display.
type CapacityReader interface {
	Capacity(ctx context.Context, zoneID string) (*ZoneCapacity, error)
}

// Capacity reports a zone's headroom in whichever form its provider uses.
//
// For a node pool this is live per-node free memory, so the UI can show what
// "automatic" is choosing between. For an elastic provider it would be usage
// against quota and cost — node-level numbers are meaningless there, since the
// provider picks the machine and never tells you which.
func (c *ClusterScheduler) Capacity(ctx context.Context, zoneID string) (*ZoneCapacity, error) {
	zone, err := c.zones.GetByID(ctx, zoneID)
	if err != nil {
		return nil, fmt.Errorf("load zone %q: %w", zoneID, err)
	}

	switch zone.Type {
	case models.ZoneTypeProxmox:
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
		return &ZoneCapacity{Kind: CapacityKindNodePool, Nodes: out}, nil

	case models.ZoneTypeGCP, models.ZoneTypeAWS, models.ZoneTypeAzure:
		// Reported honestly as an elastic provider with nothing measured yet,
		// rather than as a node pool with zero nodes — the difference tells the
		// UI to hide node selection entirely instead of showing an empty picker.
		//
		// Wiring real numbers means querying each provider's quota and billing
		// APIs. That is deliberately not done while the GCP/AWS terraform
		// modules are still skeletons that create no resources: there is no
		// usage to report yet.
		return &ZoneCapacity{
			Kind: CapacityKindQuota,
			Note: "usage and quota reporting is not implemented for this provider yet; " +
				"placement is handled by the cloud, not by this console",
		}, nil

	default:
		return &ZoneCapacity{
			Kind: CapacityKindUnknown,
			Note: fmt.Sprintf("no capacity model for zone type %q", zone.Type),
		}, nil
	}
}

// PlacementDecider chooses the node a qube should run on.
type PlacementDecider interface {
	Place(ctx context.Context, zoneID string, req scheduler.Requirements) (*scheduler.Placement, error)
}

// ClusterScheduler resolves credentials, reads live capacity and picks a node.
type ClusterScheduler struct {
	resolve scheduler.CredentialResolver
	zones   repository.ZoneRepository
	sched   *scheduler.Scheduler
}

// NewClusterScheduler wires a scheduler onto a credential resolver.
func NewClusterScheduler(zones repository.ZoneRepository, resolve scheduler.CredentialResolver) *ClusterScheduler {
	return &ClusterScheduler{resolve: resolve, zones: zones, sched: scheduler.New()}
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
		//
		// Logged rather than swallowed. The degraded path looks identical to a
		// zone that was simply never configured, so without this line a broken
		// credential or an unreachable cluster is indistinguishable from an
		// unconfigured one — and the operator only finds out at provision time,
		// from an error that names the wrong cause.
		if err != nil {
			log.Printf("scheduler: cluster placement unavailable for zone %q, falling back to the zone default: %v", zone.Name, err)
		} else if placement == nil || placement.Node == "" {
			log.Printf("scheduler: cluster returned no node for zone %q, falling back to the zone default", zone.Name)
		}
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
