package db

import (
	"container/list"
	"encoding/json"
	"sync"
)

// decodedLogEvents holds the decoded text of log events a page stopped inside,
// so the page that resumes one serves its window without reading and decoding
// the stored event again. Without it, walking an 8 MiB log event 64 KiB at a
// time reads and decodes the whole event on each of 128 pages.
var decodedLogEvents = newLogEventCache(64 * 1024 * 1024)

// logEventKey names one stored row version. Build events are append-only, but
// the key does not lean on that: xmin changes with every row version and
// transaction IDs are cluster-wide, so a hit is the very row the page's
// snapshot listed, never an earlier row under the same build and event IDs.
type logEventKey struct {
	table string
	build int
	event int
	xmin  string
	size  int
}

// decodedLogEvent is a log event's metadata without its payload, and its
// decoded payload text. Every page that hits it shares it, so neither is
// written after it is stored.
type decodedLogEvent struct {
	metadata map[string]json.RawMessage
	text     string
}

// logEventCache is a least-recently-used set of decoded log events, bounded by
// the bytes they hold. It is safe for concurrent pages.
type logEventCache struct {
	mu      sync.Mutex
	limit   int
	used    int
	hits    int        // pages a held event served; specs read it
	order   *list.List // of *logEventEntry, most recently used first
	entries map[logEventKey]*list.Element
}

type logEventEntry struct {
	key   logEventKey
	event decodedLogEvent
	cost  int
}

func newLogEventCache(limit int) *logEventCache {
	return &logEventCache{limit: limit, order: list.New(), entries: map[logEventKey]*list.Element{}}
}

func (c *logEventCache) get(key logEventKey) (decodedLogEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, found := c.entries[key]
	if !found {
		return decodedLogEvent{}, false
	}
	c.order.MoveToFront(element)
	c.hits++
	return element.Value.(*logEventEntry).event, true
}

// put stores event under key, evicting the least recently used entries until
// the bytes held fit the limit. An event larger than the limit is not stored.
func (c *logEventCache) put(key logEventKey, event decodedLogEvent) {
	cost := len(event.text)
	for name, value := range event.metadata {
		cost += len(name) + len(value)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.entries[key]; found || cost > c.limit {
		return
	}
	for c.used+cost > c.limit {
		oldest := c.order.Remove(c.order.Back()).(*logEventEntry)
		delete(c.entries, oldest.key)
		c.used -= oldest.cost
	}
	c.entries[key] = c.order.PushFront(&logEventEntry{key: key, event: event, cost: cost})
	c.used += cost
}
