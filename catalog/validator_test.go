package catalog_test

import (
	"testing"

	"github.com/xraph/relay/catalog"
)

func TestValidatorNilSchema(t *testing.T) {
	v := catalog.NewValidator()

	if err := v.Validate(nil, map[string]any{"key": "value"}); err != nil {
		t.Fatal("nil schema should skip validation, got:", err)
	}
}

func TestValidatorValidPayload(t *testing.T) {
	v := catalog.NewValidator()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"amount":   map[string]any{"type": "number"},
			"currency": map[string]any{"type": "string"},
		},
		"required": []any{"amount", "currency"},
	}

	data := map[string]any{
		"amount":   100.50,
		"currency": "USD",
	}

	if err := v.Validate(schema, data); err != nil {
		t.Fatal("valid payload should pass, got:", err)
	}
}

func TestValidatorMissingRequired(t *testing.T) {
	v := catalog.NewValidator()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []any{"name"},
	}

	data := map[string]any{
		"other": "value",
	}

	if err := v.Validate(schema, data); err == nil {
		t.Fatal("expected validation error for missing required field")
	}
}

func TestValidatorWrongType(t *testing.T) {
	v := catalog.NewValidator()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
		},
	}

	data := map[string]any{
		"count": "not-a-number",
	}

	if err := v.Validate(schema, data); err == nil {
		t.Fatal("expected validation error for wrong type")
	}
}

func TestValidatorCaching(t *testing.T) {
	v := catalog.NewValidator()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
		},
	}

	data := map[string]any{"x": "hello"}

	// First call compiles the schema.
	if err := v.Validate(schema, data); err != nil {
		t.Fatal(err)
	}

	// Second call should use cached schema.
	if err := v.Validate(schema, data); err != nil {
		t.Fatal(err)
	}
}
