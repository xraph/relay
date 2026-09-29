package dlq

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// ErrPayloadNotJSON is returned when an entry's payload is given as bytes that
// are not JSON text.
var ErrPayloadNotJSON = errors.New("dlq: payload bytes are not JSON")

// EncodePayload returns p as JSON text, which is the form every store keeps a
// payload in and hands back as a json.RawMessage. Bytes, whether []byte or
// json.RawMessage, are taken to be JSON text already: that is what PushFailed
// produces. Any other value is marshalled.
//
// Stores used to marshal the payload themselves. Marshalling []byte yields a
// base64 string, so postgres, sqlite and redis kept the event's data as a
// quoted base64 string rather than as the JSON it was.
func EncodePayload(p any) (json.RawMessage, error) {
	switch v := p.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case json.RawMessage:
		return rawJSON(v)
	case []byte:
		return rawJSON(v)
	}
	return json.Marshal(p)
}

func rawJSON(b []byte) (json.RawMessage, error) {
	if !json.Valid(b) {
		return nil, ErrPayloadNotJSON
	}
	return json.RawMessage(b), nil
}

// PayloadJSON returns a payload read from a store as JSON, for showing to
// someone. It also reads entries written before EncodePayload existed, which
// postgres, sqlite and redis kept as a base64 string of the JSON: a string
// whose base64 decodes to a JSON object or array is taken to be one of those.
// A payload that really is such a string is vanishingly unlikely, and the cost
// of guessing wrong is showing its decoded form.
func PayloadJSON(p any) json.RawMessage {
	b, err := EncodePayload(p)
	if err != nil {
		return json.RawMessage("null")
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return b
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(decoded) == 0 || !json.Valid(decoded) {
		return b
	}
	if decoded[0] != '{' && decoded[0] != '[' {
		return b
	}
	return json.RawMessage(decoded)
}
