// Package cursor encodes the position of a row in a list ordered by
// created_at descending, then id descending.
//
// Both halves are needed. created_at alone is not a total order: two rows in
// the same instant straddling a page boundary would each be skipped or
// repeated. The id breaks the tie.
package cursor

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for anything Encode did not produce.
var ErrInvalid = errors.New("relay: invalid cursor")

// Encode returns an opaque cursor for the row at (createdAt, id).
func Encode(createdAt time.Time, id string) string {
	raw := strconv.FormatInt(createdAt.UnixNano(), 10) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode reverses Encode.
func Decode(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return time.Time{}, "", ErrInvalid
	}
	nanos, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrInvalid
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrInvalid
	}
	return time.Unix(0, n).UTC(), id, nil
}

// Limit clamps a requested page size: 0 means 50, and nothing above 200.
func Limit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	default:
		return n
	}
}
