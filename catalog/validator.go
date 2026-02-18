package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validator validates event payloads against JSON Schema definitions.
type Validator struct {
	mu    sync.RWMutex
	cache map[string]*jsonschema.Schema // keyed by schema JSON content
}

// NewValidator creates a new schema validator.
func NewValidator() *Validator {
	return &Validator{
		cache: make(map[string]*jsonschema.Schema),
	}
}

// Validate checks the given data against the schema. If schema is nil, validation is skipped.
func (v *Validator) Validate(schema, data any) error {
	if schema == nil {
		return nil
	}

	compiled, err := v.compile(schema)
	if err != nil {
		return fmt.Errorf("schema compilation error: %w", err)
	}

	return compiled.Validate(data)
}

// compile returns a compiled schema, using the cache for previously-seen schemas.
func (v *Validator) compile(schema any) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	key := string(raw)

	v.mu.RLock()
	if cached, ok := v.cache[key]; ok {
		v.mu.RUnlock()
		return cached, nil
	}
	v.mu.RUnlock()

	// Parse the schema JSON into an any value for the compiler.
	var doc any
	if unmarshalErr := json.Unmarshal(raw, &doc); unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", unmarshalErr)
	}

	// Use a unique URL as the schema resource identifier.
	url := "relay://schema/" + sanitizeKey(key)

	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(url, doc); addErr != nil {
		return nil, fmt.Errorf("add schema resource: %w", addErr)
	}

	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}

	v.mu.Lock()
	v.cache[key] = compiled
	v.mu.Unlock()

	return compiled, nil
}

// sanitizeKey creates a safe URL path segment from a schema key.
func sanitizeKey(key string) string {
	r := strings.NewReplacer(
		`"`, "",
		`{`, "",
		`}`, "",
		` `, "_",
		`:`, "",
	)
	s := r.Replace(key)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
