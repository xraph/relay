package memory

import (
	"bytes"
	"maps"
	"slices"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
)

// Copy mutable catalog and endpoint fields at the storage boundary so the
// acceptance lock also protects reads from concurrent service-level edits.
func copyEndpoint(ep *endpoint.Endpoint) *endpoint.Endpoint {
	cp := *ep
	cp.EventTypes = slices.Clone(ep.EventTypes)
	cp.Headers = maps.Clone(ep.Headers)
	cp.Metadata = maps.Clone(ep.Metadata)
	return &cp
}

func copyEventType(et *catalog.EventType) *catalog.EventType {
	cp := *et
	cp.Definition.Schema = bytes.Clone(et.Definition.Schema)
	cp.Definition.Example = bytes.Clone(et.Definition.Example)
	cp.Metadata = maps.Clone(et.Metadata)
	if et.DeprecatedAt != nil {
		at := *et.DeprecatedAt
		cp.DeprecatedAt = &at
	}
	return &cp
}
