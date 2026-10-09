// Package acceptanceobs carries private, per-call acceptance telemetry.
package acceptanceobs

import (
	"context"
	"sync/atomic"
)

type commitObservationKey struct{}

// CommitObservation is transient telemetry evidence, not a durable receipt.
// A caller must also receive success from AcceptEvent before counting it.
type CommitObservation struct{ created atomic.Bool }

// Created reports whether a supporting store observed a new commit in this call.
// False cannot establish that no earlier commit occurred.
func (o *CommitObservation) Created() bool { return o.created.Load() }

// WithCommitObservation lets supporting stores report a new commit without
// bypassing AcceptEvent wrappers or adding mutable fields to retained receipts.
func WithCommitObservation(ctx context.Context) (context.Context, *CommitObservation) {
	observation := new(CommitObservation)
	return context.WithValue(ctx, commitObservationKey{}, observation), observation
}

// MarkNewCommit is called by a store only after a new acceptance commits. Receipt
// recovery must not call it. Store adapters should forward the caller's context.
func MarkNewCommit(ctx context.Context) {
	if observation, ok := ctx.Value(commitObservationKey{}).(*CommitObservation); ok {
		observation.created.Store(true)
	}
}
