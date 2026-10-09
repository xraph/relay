package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalJSON sorts object keys, preserves exact decimal values, normalizes
// equivalent numeric forms, and rejects duplicate keys and ambiguous Unicode.
func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaxDataBytes || !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > MaxDataBytes {
		return nil, ErrInvalid
	}
	return b, nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, ok = m[key]; ok {
					return nil, ErrInvalid
				}
				x, e := readValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				m[key] = x
			}
			if end, e := d.Token(); e != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				x, e := readValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, x)
			}
			if end, e := d.Token(); e != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		default:
			return nil, ErrInvalid
		}
	case json.Number:
		return normalizeNumber(string(v))
	default:
		return t, nil
	}
}

func normalizeNumber(s string) (json.Number, error) {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mantissa, exponent := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		n, e := strconv.Atoi(s[i+1:])
		if e != nil || n < -10000 || n > 10000 {
			return "", ErrInvalid
		}
		exponent = n
	}
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		exponent -= len(mantissa) - i - 1
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0", nil
	}
	trimmed := strings.TrimRight(mantissa, "0")
	exponent += len(mantissa) - len(trimmed)
	mantissa = trimmed
	if negative {
		mantissa = "-" + mantissa
	}
	if exponent != 0 {
		mantissa += "e" + strconv.Itoa(exponent)
	}
	return json.Number(mantissa), nil
}

func validEscapes(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil || n == 0 {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			n, e = strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || n < 0xDC00 || n > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
