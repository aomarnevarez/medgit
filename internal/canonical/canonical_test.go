package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestKeyOrderDoesNotChangeTheHash(t *testing.T) {
	left := map[string]any{"b": 1, "a": map[string]any{"d": true, "c": "ñ"}}
	right := map[string]any{"a": map[string]any{"c": "ñ", "d": true}, "b": 1}
	leftHash, err := Hash(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := Hash(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("hashes differ: %s %s", leftHash, rightHash)
	}
	raw, err := Bytes(left)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"a":{"c":"ñ","d":true},"b":1}` {
		t.Fatalf("canonical bytes = %s", raw)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "3942fef6c9af3ae94e64bc9d1eebfd8efdf27cc5a20369ade417e1e8b4ce5478" {
		t.Fatalf("hash = %s", hex.EncodeToString(sum[:]))
	}
}

func TestFloatsAreRejected(t *testing.T) {
	_, err := Bytes(map[string]any{"value": 1.2})
	if err == nil || !strings.Contains(err.Error(), "floats are not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestStringEscapes(t *testing.T) {
	raw, err := Bytes(map[string]any{"s": "a\"b\\c\n\x01"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"s":"a\"b\\c\n\u0001"}` {
		t.Fatalf("canonical bytes = %s", raw)
	}
}

func TestDecodeKeepsDecimalTokensAsStrings(t *testing.T) {
	value, err := Decode([]byte(`{"value":6.3,"count":2}`))
	if err != nil {
		t.Fatal(err)
	}
	object := value.(map[string]any)
	if object["value"] != "6.3" {
		t.Fatalf("value = %#v", object["value"])
	}
	if object["count"] != int64(2) {
		t.Fatalf("count = %#v", object["count"])
	}
	_, err = DecodeStrict([]byte(`{"value":6.3}`))
	if err == nil {
		t.Fatal("strict decode accepted a float")
	}
}
