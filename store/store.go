// Package store defines the composite Store interface for all Relay persistence.
//
// The composite store follows the ControlPlane pattern: each subsystem defines
// its own store interface, and the aggregate Store composes them all.
package store

import (
	"context"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
)

// WakeNotifier is an optional store capability: backends that can push
// new-work signals (e.g. Postgres LISTEN/NOTIFY) implement it so delivery
// engines on other instances wake immediately instead of waiting out their
// idle poll backoff. Polling remains the correctness mechanism — a missed
// wake only costs poll latency.
type WakeNotifier interface {
	// StartWakeListener subscribes to the backend's wake signal and calls
	// wake for every notification until stop is invoked. Implementations
	// must survive connection loss by re-subscribing internally.
	StartWakeListener(ctx context.Context, wake func()) (stop func(), err error)
}

// Store is the aggregate persistence interface.
// Each subsystem store is a composable interface — same pattern as ControlPlane.
type Store interface {
	catalog.Store
	endpoint.Store
	event.Store
	delivery.Store
	dlq.Store

	// Migrate runs all schema migrations.
	Migrate(ctx context.Context) error

	// Ping checks database connectivity.
	Ping(ctx context.Context) error

	// Close closes the store connection.
	Close() error
}
