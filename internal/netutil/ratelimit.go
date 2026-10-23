package netutil

import (
	"container/list"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	DefaultLRUCapacity = 65536
	FailureWindow      = 1 * time.Minute
	LockoutDuration    = 15 * time.Minute
	MaxFailures        = 5
)

type rateLimitEntry struct {
	key         string
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

// RateLimiter implements a concurrency-safe LRU rate limiter.
type RateLimiter struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	evict    *list.List
}

// NewRateLimiter creates a RateLimiter with bounded capacity.
func NewRateLimiter(capacity int) *RateLimiter {
	if capacity <= 0 {
		capacity = DefaultLRUCapacity
	}
	return &RateLimiter{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		evict:    list.New(),
	}
}

func (rl *RateLimiter) makeKey(token string, clientIP net.IP) string {
	return fmt.Sprintf("%s|%s", token, RateLimitKey(clientIP))
}

// IsLocked checks whether the client is currently locked out.
// Returns (true, remainingSeconds) if locked, or (false, 0) if allowed.
func (rl *RateLimiter) IsLocked(token string, clientIP net.IP) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := rl.makeKey(token, clientIP)
	elem, exists := rl.items[key]
	if !exists {
		return false, 0
	}

	rl.evict.MoveToFront(elem)
	entry := elem.Value.(*rateLimitEntry)

	now := time.Now()
	if now.Before(entry.lockedUntil) {
		remaining := int(entry.lockedUntil.Sub(now).Seconds())
		if remaining <= 0 {
			remaining = 1
		}
		return true, remaining
	}

	return false, 0
}

// RecordFailure records a failed authentication or invalid attempt.
// Returns (true, remainingSeconds) if this failure caused or keeps a lockout.
func (rl *RateLimiter) RecordFailure(token string, clientIP net.IP) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := rl.makeKey(token, clientIP)
	now := time.Now()

	elem, exists := rl.items[key]
	var entry *rateLimitEntry
	if exists {
		rl.evict.MoveToFront(elem)
		entry = elem.Value.(*rateLimitEntry)
	} else {
		// Evict oldest if full
		if rl.evict.Len() >= rl.capacity {
			oldest := rl.evict.Back()
			if oldest != nil {
				rl.evict.Remove(oldest)
				oldEntry := oldest.Value.(*rateLimitEntry)
				delete(rl.items, oldEntry.key)
			}
		}

		entry = &rateLimitEntry{
			key:         key,
			failures:    0,
			windowStart: now,
		}
		elem = rl.evict.PushFront(entry)
		rl.items[key] = elem
	}

	// If currently locked out
	if now.Before(entry.lockedUntil) {
		remaining := int(entry.lockedUntil.Sub(now).Seconds())
		if remaining <= 0 {
			remaining = 1
		}
		return true, remaining
	}

	// Check 1-minute window
	if now.Sub(entry.windowStart) > FailureWindow {
		entry.failures = 0
		entry.windowStart = now
	}

	entry.failures++
	if entry.failures >= MaxFailures {
		entry.lockedUntil = now.Add(LockoutDuration)
		return true, int(LockoutDuration.Seconds())
	}

	return false, 0
}

// Reset clears any failure count and lockout for the token and clientIP upon successful authentication.
func (rl *RateLimiter) Reset(token string, clientIP net.IP) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := rl.makeKey(token, clientIP)
	if elem, exists := rl.items[key]; exists {
		rl.evict.Remove(elem)
		delete(rl.items, key)
	}
}
