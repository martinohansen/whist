package main

import (
	"sync"
	"time"
)

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]rateLimitEntry
}

type rateLimitEntry struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:  limit,
		window: window,
		hits:   make(map[string]rateLimitEntry),
	}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	if l == nil || l.limit <= 0 || l.window <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.hits[key]
	if entry.start.IsZero() || now.Sub(entry.start) >= l.window {
		l.hits[key] = rateLimitEntry{start: now, count: 1}
		l.pruneLocked(now)
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.hits[key] = entry
	return true
}

func (l *rateLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.hits {
		if now.Sub(entry.start) >= l.window {
			delete(l.hits, key)
		}
	}
}
