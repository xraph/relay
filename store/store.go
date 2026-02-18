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
