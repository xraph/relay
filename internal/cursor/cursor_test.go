package cursor_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/xraph/relay/internal/cursor"
)

func TestRoundTripKeepsNanoseconds(t *testing.T) {
	at := time.Date(2026, 9, 23, 12, 0, 0, 123456789, time.UTC)
	gotAt, gotID, err := cursor.Decode(cursor.Encode(at, "del_01abc"))
	if err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(at) || gotID != "del_01abc" {
		t.Errorf("decoded (%v, %q), want (%v, %q)", gotAt, gotID, at, "del_01abc")
	}
}

func TestDecodeRefusesWhatEncodeDidNotMake(t *testing.T) {
	for _, s := range []string{
		"",
		"!!",
		base64.RawURLEncoding.EncodeToString([]byte("abc")),
		base64.RawURLEncoding.EncodeToString([]byte("123|")),
		base64.RawURLEncoding.EncodeToString([]byte("notanumber|del_1")),
	} {
		if _, _, err := cursor.Decode(s); !errors.Is(err, cursor.ErrInvalid) {
			t.Errorf("Decode(%q) = %v, want ErrInvalid", s, err)
		}
	}
}

func TestLimit(t *testing.T) {
	for in, want := range map[int]int{0: 50, -1: 50, 1: 1, 200: 200, 5000: 200} {
		if got := cursor.Limit(in); got != want {
			t.Errorf("Limit(%d) = %d, want %d", in, got, want)
		}
	}
}
