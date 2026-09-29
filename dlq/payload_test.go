package dlq

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestEncodePayload(t *testing.T) {
	const obj = `{"id":"inv_1"}`
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"bytes are JSON text", []byte(obj), obj},
		{"raw JSON is kept", json.RawMessage(obj), obj},
		{"a value is marshalled", map[string]any{"id": "inv_1"}, obj},
		{"a string is a JSON string", "inv_1", `"inv_1"`},
		{"nil is null", nil, `null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EncodePayload(c.in)
			if err != nil {
				t.Fatalf("EncodePayload: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}

	t.Run("bytes that are not JSON are refused", func(t *testing.T) {
		if _, err := EncodePayload([]byte("not json")); !errors.Is(err, ErrPayloadNotJSON) {
			t.Fatalf("error = %v, want ErrPayloadNotJSON", err)
		}
	})
}

func TestPayloadJSONReadsEntriesWrittenBeforeTheFix(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		// What postgres and sqlite hand back for a row the old code wrote:
		// the JSON of the base64 of the JSON.
		{"legacy object", json.RawMessage(`"eyJpZCI6Imludl8xIn0="`), `{"id":"inv_1"}`},
		{"legacy array", json.RawMessage(`"WzEsMl0="`), `[1,2]`},
		{"current payload", json.RawMessage(`{"id":"inv_1"}`), `{"id":"inv_1"}`},
		// A real string payload stays a string, even one that is valid
		// base64, unless it decodes to an object or array.
		{"a string payload", json.RawMessage(`"hello"`), `"hello"`},
		{"base64 of a scalar", json.RawMessage(`"MTIz"`), `"MTIz"`},
		{"nothing", nil, `null`},
		{"bytes that are not JSON", []byte("not json"), `null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PayloadJSON(c.in); string(got) != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}
