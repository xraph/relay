// Package observability holds relay's metric instruments. Tracing needs
// nothing from it: relay starts its spans on the global tracer provider,
// which forge.New installs.
package observability
