// Package errs holds the sentinel errors that packages below the root need to
// recognise. The root package re-exports each one under its public name, so
// errors.Is against relay.ErrEndpointNotFound and errs.ErrEndpointNotFound is the
// same comparison.
package errs

import "errors"

var (
	// ErrEndpointNotFound is relay.ErrEndpointNotFound.
	ErrEndpointNotFound = errors.New("relay: endpoint not found")

	// ErrEventNotFound is relay.ErrEventNotFound.
	ErrEventNotFound = errors.New("relay: event not found")

	// ErrInvalidFilter is relay.ErrInvalidFilter.
	ErrInvalidFilter = errors.New("relay: invalid filter")
)
