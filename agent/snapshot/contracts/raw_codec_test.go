package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

// This catches a regression where raw author input can choose an unregistered
// record contract or smuggle fields that the typed contract does not accept.
func TestBuiltinRawRecordCodecIsClosedAndStrict(t *testing.T) {
	codec, found := BuiltinRawRecordCodec(reviewType)
	if !found {
		t.Fatal("review/v1 has no raw codec")
	}
	if _, err := codec.DecodeBody(json.RawMessage(`{"conclusion":"accept","summary":"ready","findings":[],"forged":true}`)); err == nil {
		t.Fatal("raw codec accepted an unknown review body field")
	}
	if _, found := BuiltinRawRecordCodec("opaque/v1"); found {
		t.Fatal("raw codec exposed a non-record output contract")
	}
	if _, _, err := NormalizeRawRecordBody("opaque/v1", nil, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "no built-in raw record codec") {
		t.Fatalf("NormalizeRawRecordBody(opaque) error = %v", err)
	}
}
