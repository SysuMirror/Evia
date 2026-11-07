package ingestion

import (
	"context"
	"sync"
	"time"
)

// CachedSources bounds memory and successful credential staleness. Unknown keys
// are not cached, so arbitrary public key guesses cannot consume cache entries.
// A cache generation prevents an in-flight fetch from undoing local revocation.
type CachedSources struct {
	store      SourceResolver
	mu         sync.Mutex
	entries    map[string]sourceEntry
	generation uint64
	ttl        time.Duration
	max        int
	misses     chan struct{}
}
type sourceEntry struct {
	value   AdmissionSource
	expires time.Time
}

func NewCachedSources(store SourceResolver) *CachedSources {
	return &CachedSources{store: store, entries: map[string]sourceEntry{}, ttl: 2 * time.Second, max: 4096, misses: make(chan struct{}, 8)}
}
func (c *CachedSources) Clear() {
	c.mu.Lock()
	c.entries = map[string]sourceEntry{}
	c.generation++
	c.mu.Unlock()
}
func (c *CachedSources) ResolveSource(ctx context.Context, key string) (AdmissionSource, error) {
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	generation := c.generation
	c.mu.Unlock()
	if ok && entry.expires.After(now) && (entry.value.Credential.ExpiresAt == nil || entry.value.Credential.ExpiresAt.After(now)) {
		return entry.value, nil
	}
	select {
	case c.misses <- struct{}{}:
		defer func() { <-c.misses }()
	default:
		return AdmissionSource{}, unavailable("SOURCE_CONFIGURATION_BUSY")
	}
	lookup, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	v, e := c.store.ResolveSource(lookup, key)
	if e != nil {
		return v, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation == generation {
		if len(c.entries) >= c.max {
			for k, v := range c.entries {
				if !v.expires.After(now) {
					delete(c.entries, k)
				}
			}
			if len(c.entries) >= c.max {
				for k := range c.entries {
					delete(c.entries, k)
					break
				}
			}
		}
		c.entries[key] = sourceEntry{v, now.Add(c.ttl)}
	}
	return v, nil
}
