package service

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/scheduler"
)

// NewTerraformEnvFunc supplies terraform's provider credentials from the
// encrypted credential store.
//
// Credentials are passed as environment variables of the terraform process, not
// as terraform variables: a value supplied as a variable is written into state
// in plaintext, and this repository's state design forbids long-lived
// credentials entering state at all. The variable names are the ones
// bpg/proxmox reads.
//
// Note the terraform root module declares a SINGLE proxmox provider, so it can
// only ever authenticate to one cluster. That is why this resolves the one
// proxmox zone rather than taking a zone id: there is nothing per-qube to vary.
func NewTerraformEnvFunc(zoneRepo repository.ZoneRepository, secrets SecretReader) orchestrator.EnvFunc {
	return func(ctx context.Context) ([]string, error) {
		zone, err := singleProxmoxZone(ctx, zoneRepo)
		if err != nil {
			return nil, err
		}
		// No zone configured yet is not an error: terraform may legitimately be
		// invoked with nothing to do. Returning no variables lets whatever the
		// operator has in their own environment still work.
		if zone == nil {
			return nil, nil
		}

		pc := zone.Config.Proxmox
		if pc == nil || pc.CredentialID == "" {
			return nil, nil
		}

		secret, err := secrets.GetSecret(ctx, pc.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("zone %q: read credential: %w", zone.Name, err)
		}
		creds := parseProxmoxSecret(secret)
		creds.Endpoint = zone.Config.Endpoint
		if !creds.Valid() {
			return nil, fmt.Errorf(
				"zone %q: stored credential is not a usable Proxmox secret", zone.Name)
		}
		return proxmoxEnv(creds), nil
	}
}

// proxmoxEnv renders credentials as the variables bpg/proxmox reads.
func proxmoxEnv(c scheduler.Credentials) []string {
	env := []string{"PROXMOX_VE_ENDPOINT=" + c.Endpoint}
	if c.APIToken != "" {
		// A token is preferred: it can be scoped and revoked without touching
		// the owning user account.
		env = append(env, "PROXMOX_VE_API_TOKEN="+c.APIToken)
	} else {
		env = append(env,
			"PROXMOX_VE_USERNAME="+c.Username,
			"PROXMOX_VE_PASSWORD="+c.Password)
	}
	if c.Insecure {
		env = append(env, "PROXMOX_VE_INSECURE=true")
	}
	return env
}

// singleProxmoxZone returns the one proxmox zone, nil if there is none, or an
// error if there are several.
//
// Ambiguity is a hard failure rather than a first-match guess: picking one of
// two clusters arbitrarily would have terraform authenticate to a cluster the
// operator did not intend, and the symptom (resources appearing somewhere
// unexpected) is far worse than a startup-time refusal. Supporting more than
// one cluster needs provider aliases in the root module, which do not exist.
func singleProxmoxZone(ctx context.Context, zoneRepo repository.ZoneRepository) (*models.Zone, error) {
	opts := repository.DefaultZoneListOptions()
	opts.Limit = 1000
	zones, err := zoneRepo.List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}

	var found []*models.Zone
	for _, z := range zones {
		if z.Type == models.ZoneTypeProxmox && z.Config.Proxmox != nil && z.Config.Proxmox.CredentialID != "" {
			found = append(found, z)
		}
	}

	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	default:
		names := make([]string, 0, len(found))
		for _, z := range found {
			names = append(names, z.Name)
		}
		return nil, fmt.Errorf(
			"%d proxmox zones have credentials (%v) but the terraform root module declares a single "+
				"provider and cannot target more than one cluster; leave a credential_id on only one",
			len(found), names)
	}
}
