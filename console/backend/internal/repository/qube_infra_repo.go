package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/provider"
)

// QubeInfraRepository persists the provider-side identity of a qube's
// infrastructure. It is the storage behind provider.Infra and replaces the
// terraform state file as the console's record of what exists.
type QubeInfraRepository struct {
	db *database.DB
}

// NewQubeInfraRepository creates a QubeInfraRepository.
func NewQubeInfraRepository(db *database.DB) *QubeInfraRepository {
	return &QubeInfraRepository{db: db}
}

// qubeInfraColumns is shared by every read so the SELECT and Scan orders cannot
// drift apart.
const qubeInfraColumns = `qube_id, provider, node, storage_vmid, compute_vmid,
	data_volume, identity_vol, observed_state, protected, observed_at, created_at, updated_at`

// Get returns the infra row for a qube, or (nil, nil) when none exists.
//
// A missing row is not an error: it is the honest state of a qube that has
// never had infrastructure created, and callers use it to decide between
// provisioning and adopting.
func (r *QubeInfraRepository) Get(ctx context.Context, qubeID string) (*provider.Infra, error) {
	query := `SELECT ` + qubeInfraColumns + ` FROM qube_infra WHERE qube_id = ?`

	var (
		inf                  provider.Infra
		protected            int
		observedAt           sql.NullTime
		createdAt, updatedAt time.Time
	)
	err := r.db.DB().QueryRowContext(ctx, query, qubeID).Scan(
		&inf.QubeID, &inf.Provider, &inf.Node, &inf.StorageVMID, &inf.ComputeVMID,
		&inf.DataVolume, &inf.IdentityVol, &inf.ObservedState, &protected, &observedAt,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	inf.Protected = protected != 0
	if observedAt.Valid {
		t := observedAt.Time
		inf.ObservedAt = &t
	}
	return &inf, nil
}

// Save upserts an infra row. created_at is set once, on first insert, and never
// overwritten; updated_at always follows the write.
//
// Adapters use this primitive for a write-ahead reservation before creating
// resources, and again for observed details. A later save alone cannot close
// the gap between a provider side effect and a database write.
func (r *QubeInfraRepository) Save(ctx context.Context, inf *provider.Infra) error {
	if inf == nil || inf.QubeID == "" {
		return errors.New("qube infra: QubeID is required")
	}
	now := time.Now().UTC()
	protected := 0
	if inf.Protected {
		protected = 1
	}

	query := `
		INSERT INTO qube_infra
			(qube_id, provider, node, storage_vmid, compute_vmid, data_volume,
			 identity_vol, observed_state, protected, observed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(qube_id) DO UPDATE SET
			provider       = excluded.provider,
			node           = excluded.node,
			storage_vmid   = excluded.storage_vmid,
			compute_vmid   = excluded.compute_vmid,
			data_volume    = excluded.data_volume,
			identity_vol   = excluded.identity_vol,
			observed_state = excluded.observed_state,
			protected      = excluded.protected,
			observed_at    = excluded.observed_at,
			updated_at     = excluded.updated_at`

	_, err := r.db.DB().ExecContext(ctx, query,
		inf.QubeID, inf.Provider, inf.Node, inf.StorageVMID, inf.ComputeVMID,
		inf.DataVolume, inf.IdentityVol, inf.ObservedState, protected, inf.ObservedAt,
		now, now,
	)
	return err
}

// Delete removes a qube's infra row. It is called only after the provider has
// confirmed the underlying resources are gone (a purge), never on its own.
func (r *QubeInfraRepository) Delete(ctx context.Context, qubeID string) error {
	_, err := r.db.DB().ExecContext(ctx, `DELETE FROM qube_infra WHERE qube_id = ?`, qubeID)
	return err
}
