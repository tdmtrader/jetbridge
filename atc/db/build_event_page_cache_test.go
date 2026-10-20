package db

import (
	"encoding/json"
	"testing"
)

// The decoded log events a process holds stay within their byte bound, and the
// least recently used one is the one that leaves.
func TestDecodedLogEventsStayWithinTheirByteBound(t *testing.T) {
	cache := newLogEventCache(10)
	key := func(event int) logEventKey {
		return logEventKey{table: "t", build: 1, event: event, xmin: "7", size: 9}
	}
	held := func(event int) bool {
		_, found := cache.get(key(event))
		return found
	}

	cache.put(key(1), decodedLogEvent{text: "aaaa"})
	cache.put(key(2), decodedLogEvent{text: "bb", metadata: map[string]json.RawMessage{"o": json.RawMessage("{}")}})
	if !held(1) {
		t.Fatal("event 1 left before the bound was reached")
	}
	cache.put(key(3), decodedLogEvent{text: "cccc"})
	if held(2) {
		t.Fatal("event 2, the least recently used, is still held past the bound")
	}
	if !held(1) || !held(3) {
		t.Fatal("an event within the bound left")
	}
	if cache.used > cache.limit {
		t.Fatalf("holds %d bytes over a %d byte bound", cache.used, cache.limit)
	}
	cache.put(key(4), decodedLogEvent{text: "dddddddddddd"})
	if held(4) {
		t.Fatal("an event larger than the whole bound was stored")
	}
}
