package relay

import "github.com/xraph/relay/internal/entity"

// Entity is the base type embedded by all relay domain objects.
type Entity = entity.Entity

// NewEntity returns an Entity with both timestamps set to the current UTC time.
func NewEntity() Entity {
	return entity.New()
}
