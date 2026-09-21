package provider

import (
	"context"
	"errors"
)

type checkpointKey struct{}

// WithCheckpoint installs durable identity storage for an operation. Adapters
// must checkpoint allocated identities before the first mutating request.
func WithCheckpoint(ctx context.Context, save func(context.Context, Infra) error) context.Context {
	return context.WithValue(ctx, checkpointKey{}, save)
}

// Checkpoint refuses to mutate when no durable recorder is installed.
func Checkpoint(ctx context.Context, in Infra) error {
	save, ok := ctx.Value(checkpointKey{}).(func(context.Context, Infra) error)
	if !ok || save == nil {
		return errors.New("provider: durable identity checkpoint is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return save(ctx, in)
}
