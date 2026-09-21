package service

import (
	"context"
	"fmt"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
)

// Finish rollback even if the HTTP caller disconnected. Report persistence
// failure so the operator knows startup reconciliation may be required.
func (s *QubeServiceImpl) releaseFailedClaim(ctx context.Context, id string, status models.QubeStatus) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.qubeRepo.UpdateStatus(writeCtx, id, status); err != nil {
		return fmt.Errorf("record failed lifecycle claim: %w", err)
	}
	return nil
}

// Preparation runs after the durable claim, so restart sees a stranded operation
// even when the process exits before a job can be inserted.
func (s *QubeServiceImpl) prepareProvision(ctx context.Context, q *models.Qube) error {
	if s.issuer != nil {
		if err := s.issuer.IssueFor(ctx, q); err != nil {
			return fmt.Errorf("prepare bootstrap identity: %w", err)
		}
	}
	if s.dataKeys != nil {
		if _, err := s.dataKeys.EnsureDataKey(ctx, q.ID); err != nil {
			return fmt.Errorf("prepare data key: %w", err)
		}
	}
	return nil
}

func (s *QubeServiceImpl) prepareResume(ctx context.Context, q *models.Qube, prior models.QubeStatus) error {
	if s.infraStore != nil {
		in, err := s.infraStore.Get(ctx, q.ID)
		if err != nil {
			return err
		}
		if in == nil || in.StorageVMID == 0 {
			return s.prepareProvision(ctx, q)
		}
		if in.ComputeVMID == 0 {
			// IDs are checkpointed before create and cleared only after deletion.
			return s.reissueIdentityForResume(ctx, q, models.QubeStatusSuspended)
		}
	}
	return s.reissueIdentityForResume(ctx, q, prior)
}
