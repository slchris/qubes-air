package service

import (
	"context"
	"fmt"
	"os"
	"strings"

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
func NewTerraformEnvFunc(
	zoneRepo repository.ZoneRepository,
	secrets SecretReader,
	sshKeyFile, sshUsername string,
) orchestrator.EnvFunc {
	return func(ctx context.Context) ([]string, error) {
		zone, err := singleProxmoxZone(ctx, zoneRepo)
		if err != nil {
			return nil, err
		}
		// No proxmox zone is not an error — a GCP-only deployment is legitimate,
		// and terraform may be invoked with nothing to do at all. Fall through
		// to the GCP variables rather than returning early, which is what made
		// a GCP zone produce no environment whatsoever.
		if zone == nil || zone.Config.Proxmox == nil ||
			zone.Config.Proxmox.CredentialID == "" {
			return gcpEnvFor(ctx, zoneRepo, secrets)
		}

		secret, err := secrets.GetSecret(ctx, zone.Config.Proxmox.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("zone %q: read credential: %w", zone.Name, err)
		}
		creds := parseProxmoxSecret(secret)
		creds.Endpoint = zone.Config.Endpoint
		if !creds.Valid() {
			return nil, fmt.Errorf(
				"zone %q: stored credential is not a usable Proxmox secret", zone.Name)
		}

		env := proxmoxEnv(creds)

		// A GCP zone contributes its own variables. Both provider blocks live in
		// one root module, so a deployment with both a Proxmox and a GCP zone
		// needs both sets present in the same terraform process.
		gcpEnv, err := gcpEnvFor(ctx, zoneRepo, secrets)
		if err != nil {
			return nil, err
		}
		env = append(env, gcpEnv...)

		// Read at call time, not at startup: the key can be rotated without
		// restarting the console, and a console that started before the key
		// existed picks it up on the next job rather than needing a restart
		// nobody would connect to the failure.
		if sshKeyFile != "" {
			key, err := os.ReadFile(sshKeyFile)
			if err != nil {
				return nil, fmt.Errorf(
					"read proxmox ssh key %q: %w (uploading the cloud-init "+
						"snippet needs SSH to the node; the PVE API has no "+
						"endpoint for it)", sshKeyFile, err)
			}
			env = append(env, sshEnv(sshUsername, string(key))...)
		}
		return env, nil
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

// sshEnv supplies the provider's SSH login for the operations the PVE API
// cannot perform.
//
// Uploading a cloud-init snippet writes /var/lib/vz/snippets/ on the node over
// SSH; there is no API endpoint for it. That snippet carries the per-qube agent
// identity, so this is not an optional extra for an exotic code path — every
// provision needs it, and a cluster reachable only on 443 cannot be provisioned
// at all.
//
// Passed as TF_VAR_ rather than written into a tfvars file so the key follows
// the same rule as the API token: never on disk in the terraform root, never in
// state. The provider falls back to ssh-agent when these are empty, which does
// not exist under systemd — so an empty key here fails at apply time rather
// than silently authenticating as someone else.
func sshEnv(username, privateKey string) []string {
	if privateKey == "" {
		return nil
	}
	if username == "" {
		username = "root"
	}
	return []string{
		"TF_VAR_proxmox_ssh_username=" + username,
		"TF_VAR_proxmox_ssh_private_key=" + privateKey,
	}
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

// gcpEnvFor renders the GCP zone's credentials and placement as environment
// variables, or nothing when there is no usable GCP zone.
//
// Everything the google provider needs arrives this way — credentials AND
// project/region. The provider block in the root module is deliberately empty:
// wiring it to a tfvars variable would give one setting two sources, which is
// exactly how the proxmox endpoint came to be silently ignored while
// `terraform output` still echoed the value the operator had typed.
//
// GOOGLE_CREDENTIALS takes the service-account key JSON directly, so no gcloud
// CLI and no key file on disk are involved.
func gcpEnvFor(
	ctx context.Context, zoneRepo repository.ZoneRepository, secrets SecretReader,
) ([]string, error) {
	zone, err := singleGCPZone(ctx, zoneRepo)
	if err != nil || zone == nil {
		return nil, err
	}
	gc := zone.Config.GCP

	key, err := secrets.GetSecret(ctx, gc.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("zone %q: read credential: %w", zone.Name, err)
	}
	key = strings.TrimSpace(key)
	// A service-account key is JSON. Catching the shape here names the problem;
	// the provider's own failure is a generic auth error several minutes into an
	// apply, which reads as a permissions issue rather than a pasted-wrong-thing
	// issue.
	if !strings.HasPrefix(key, "{") {
		return nil, fmt.Errorf(
			"zone %q: stored credential is not a GCP service-account key (expected JSON)",
			zone.Name)
	}

	env := []string{"GOOGLE_CREDENTIALS=" + key}
	if p := strings.TrimSpace(zone.Config.Project); p != "" {
		env = append(env, "GOOGLE_PROJECT="+p)
	}
	if r := strings.TrimSpace(zone.Config.Region); r != "" {
		env = append(env, "GOOGLE_REGION="+r)
	}
	if z := strings.TrimSpace(gc.Zone); z != "" {
		env = append(env, "GOOGLE_ZONE="+z)
	}
	return env, nil
}

// singleGCPZone mirrors singleProxmoxZone: one google provider in the root
// module means one authenticated project, so two credentialed GCP zones are a
// hard failure rather than an arbitrary pick.
func singleGCPZone(ctx context.Context, zoneRepo repository.ZoneRepository) (*models.Zone, error) {
	opts := repository.DefaultZoneListOptions()
	opts.Limit = 1000
	zones, err := zoneRepo.List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}

	var found []*models.Zone
	for _, z := range zones {
		if z.Type == models.ZoneTypeGCP && z.Config.GCP != nil && z.Config.GCP.CredentialID != "" {
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
			"%d gcp zones have credentials (%v) but the terraform root module declares a single "+
				"google provider and cannot target more than one project; leave a credential_id on only one",
			len(found), names)
	}
}
