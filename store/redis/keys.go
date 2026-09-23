package redis

import "time"

// Key prefixes for primary entity storage.
const (
	prefixEventType = "relay:evtype:"
	prefixEndpoint  = "relay:ep:"
	prefixEvent     = "relay:evt:"
	prefixDelivery  = "relay:del:"
	prefixDLQ       = "relay:dlq:"
)

// Key prefixes for unique indexes.
const (
	uniqueEventTypeName = "relay:u:evtype:name:"
	uniqueEventIdem     = "relay:u:evt:idem:"
)

// Key prefixes for sorted set indexes.
const (
	zEventTypeAll   = "relay:z:evtype:all"
	zEventTypeGroup = "relay:z:evtype:group:" // + group name
	zEndpointTenant = "relay:z:ep:tenant:"    // + tenant ID
	zEndpointAll    = "relay:z:ep:all"        // every endpoint, across tenants
	zEventAll       = "relay:z:evt:all"
	zEventTenant    = "relay:z:evt:tenant:" // + tenant ID
	zDeliveryEP     = "relay:z:del:ep:"     // + endpoint ID
	zDeliveryEvt    = "relay:z:del:evt:"    // + event ID
	zDeliveryPend   = "relay:z:del:pending"
	zDLQAll         = "relay:z:dlq:all"
	zDLQTenant      = "relay:z:dlq:tenant:" // + tenant ID
	zDLQEndpoint    = "relay:z:dlq:ep:"     // + endpoint ID
)

// Key prefixes for set indexes.
const (
	sEventTypeActive = "relay:s:evtype:active"
	sEndpointEnabled = "relay:s:ep:tenant:" // + tenantID + ":enabled"
)

// Markers for the global endpoint index. Two keys with different jobs:
//
// migratedEndpointAllV1 says the backfill ran recently, so a normal boot skips
// it. It expires, so the backfill re-runs now and then and repairs the index
// after a rollback to a version that did not maintain it.
//
// endpointIndexBuilt says the index has been built at least once and can be
// trusted. It never expires. An every-tenant list refuses to answer without
// it, because an unbuilt index looks exactly like "no endpoints".
const (
	migratedEndpointAllV1 = "relay:migrated:ep_all:v1"
	endpointIndexBuilt    = "relay:migrated:ep_all:built"

	endpointBackfillTTL = 24 * time.Hour
)

// entityKey returns the primary key for an entity.
func entityKey(prefix, id string) string {
	return prefix + id
}

// enabledSetKey returns the set key for enabled endpoints of a tenant.
func enabledSetKey(tenantID string) string {
	return sEndpointEnabled + tenantID + ":enabled"
}
