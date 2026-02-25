package redis

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

// entityKey returns the primary key for an entity.
func entityKey(prefix, id string) string {
	return prefix + id
}

// enabledSetKey returns the set key for enabled endpoints of a tenant.
func enabledSetKey(tenantID string) string {
	return sEndpointEnabled + tenantID + ":enabled"
}
