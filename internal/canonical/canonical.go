// Package canonical writes the UTF-8 JSON that MedGit hashes.
//
// Object keys are sorted by Unicode code point, separators are comma and colon
// with no extra whitespace, and the encoding is UTF-8 with no HTML escaping.
// Floats are rejected. Decimal clinical values are strings so two writers
// cannot hash the same fact to different digests.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const floatMsg = "floats are not allowed in MedGit objects; store decimals as strings"

// Bytes returns canonical JSON for v.
func Bytes(v any) ([]byte, error) {
	return appendValue(nil, v)
}

// Hash is the hex SHA-256 of the canonical JSON for v.
func Hash(v any) (string, error) {
	raw, err := Bytes(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Decode parses JSON and stores decimal numbers as strings. Integers stay integers.
func Decode(data []byte) (any, error) {
	return decode(data, true)
}

// DecodeStrict parses JSON and rejects decimal numbers.
func DecodeStrict(data []byte) (any, error) {
	return decode(data, false)
}

// CoerceValue walks a decoded tree and applies the same number rules as Decode.
func CoerceValue(v any) (any, error) {
	return coerce(v, true)
}

func decode(data []byte, coerceDecimals bool) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("extra data after JSON")
		}
		return nil, err
	}
	return coerce(v, coerceDecimals)
}

func coerce(v any, coerceDecimals bool) (any, error) {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if strings.ContainsAny(s, ".eE") {
			if !coerceDecimals {
				return nil, fmt.Errorf("%s", floatMsg)
			}
			return s, nil
		}
		i, err := t.Int64()
		if err != nil {
			return nil, err
		}
		return i, nil
	case float32, float64:
		return nil, fmt.Errorf("%s", floatMsg)
	case map[string]any:
		for key, child := range t {
			next, err := coerce(child, coerceDecimals)
			if err != nil {
				return nil, err
			}
			t[key] = next
		}
		return t, nil
	case []any:
		for i, child := range t {
			next, err := coerce(child, coerceDecimals)
			if err != nil {
				return nil, err
			}
			t[i] = next
		}
		return t, nil
	default:
		return v, nil
	}
}

// Marshal encodes v as JSON without HTML escaping.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// MarshalIndent encodes v as indented JSON without HTML escaping.
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func appendValue(dst []byte, v any) ([]byte, error) {
	if v == nil {
		return append(dst, "null"...), nil
	}
	switch t := v.(type) {
	case bool:
		if t {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case string:
		return appendString(dst, t), nil
	case int:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case int8:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case int16:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case int32:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case int64:
		return strconv.AppendInt(dst, t, 10), nil
	case uint:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	case uint8:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	case uint16:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	case uint32:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	case uint64:
		return strconv.AppendUint(dst, t, 10), nil
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			return nil, fmt.Errorf("%s", floatMsg)
		}
		i, err := t.Int64()
		if err != nil {
			return nil, err
		}
		return strconv.AppendInt(dst, i, 10), nil
	case float32, float64:
		return nil, fmt.Errorf("%s", floatMsg)
	case []any:
		return appendArray(dst, t)
	case []map[string]any:
		dst = append(dst, '[')
		for i, item := range t {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendValue(dst, item)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case map[string]any:
		return appendObject(dst, t)
	default:
		return nil, fmt.Errorf("unsupported value type: %T", v)
	}
}

func appendArray(dst []byte, items []any) ([]byte, error) {
	dst = append(dst, '[')
	for i, item := range items {
		if i > 0 {
			dst = append(dst, ',')
		}
		var err error
		dst, err = appendValue(dst, item)
		if err != nil {
			return nil, err
		}
	}
	return append(dst, ']'), nil
}

func appendObject(dst []byte, obj map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dst = append(dst, '{')
	for i, key := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendString(dst, key)
		dst = append(dst, ':')
		var err error
		dst, err = appendValue(dst, obj[key])
		if err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for _, r := range s {
		switch r {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if r < 0x20 {
				dst = append(dst, fmt.Sprintf("\\u%04x", r)...)
			} else {
				dst = append(dst, string(r)...)
			}
		}
	}
	return append(dst, '"')
}
