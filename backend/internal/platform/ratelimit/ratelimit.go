// Package ratelimit is an in-process token-bucket limiter keyed by client address (FND-26).
//
// Each API instance keeps its own counters: with N instances behind a load balancer a client can make up to N times
// the configured rate. A Redis-backed limiter is the planned replacement when that matters.
package ratelimit

import (
	"container/list"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// Rule is a rate: Burst requests at once, refilled evenly so that PerMinute requests are allowed per minute.
type Rule struct {
	PerMinute int
	Burst     int
}

// Limiter is a set of token buckets, one per key. It is safe for concurrent use. Memory is bounded: at most MaxKeys
// buckets are kept, and the least recently used one is dropped to make room, so an attacker who rotates addresses
// can raise the cost of their own requests but cannot grow the server's memory.
type Limiter struct {
	rule    Rule
	maxKeys int
	now     func() time.Time

	mu      sync.Mutex
	buckets map[string]*list.Element // value: *bucket
	order   *list.List               // front = most recently used
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

// New returns a limiter. maxKeys below 1 is treated as 1. now is time.Now in production.
func New(rule Rule, maxKeys int, now func() time.Time) *Limiter {
	if maxKeys < 1 {
		maxKeys = 1
	}
	if rule.PerMinute < 1 {
		rule.PerMinute = 1
	}
	if rule.Burst < 1 {
		rule.Burst = 1
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{rule: rule, maxKeys: maxKeys, now: now, buckets: map[string]*list.Element{}, order: list.New()}
}

// Len reports how many keys are currently tracked.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// refillPerSecond is the token refill rate.
func (l *Limiter) refillPerSecond() float64 { return float64(l.rule.PerMinute) / 60 }

// Allow takes one token for key. When none is left it reports false and how long to wait for the next one.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	var b *bucket
	if el, found := l.buckets[key]; found {
		b = el.Value.(*bucket)
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(float64(l.rule.Burst), b.tokens+elapsed*l.refillPerSecond())
			b.last = now
		}
		l.order.MoveToFront(el)
	} else {
		for len(l.buckets) >= l.maxKeys {
			l.evict(l.order.Back())
		}
		b = &bucket{key: key, tokens: float64(l.rule.Burst), last: now}
		l.buckets[key] = l.order.PushFront(b)
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.refillPerSecond() * float64(time.Second))
	// Round up to a whole millisecond so float error never reports a wait that is a hair too short.
	return false, (wait + time.Millisecond - 1) / time.Millisecond * time.Millisecond
}

// sweep drops buckets that have been idle long enough to be full again: forgetting them changes no decision. It looks
// only at the least recently used end, so the cost per call is small and amortised.
func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(float64(l.rule.Burst) / l.refillPerSecond() * float64(time.Second))
	for i := 0; i < 16; i++ {
		el := l.order.Back()
		if el == nil || now.Sub(el.Value.(*bucket).last) < full {
			return
		}
		l.evict(el)
	}
}

func (l *Limiter) evict(el *list.Element) {
	if el == nil {
		return
	}
	delete(l.buckets, el.Value.(*bucket).key)
	l.order.Remove(el)
}

// ClientKey turns a client address into a bucket key. IPv6 clients are grouped by /64, the smallest block an ISP
// hands out, so one client cannot dodge the limit by rotating addresses inside its own block. Anything that is not an
// IP address is used as is (but lower-cased and trimmed) so it still lands in one bucket.
func ClientKey(addr string) string {
	addr = strings.TrimSpace(addr)
	ip := net.ParseIP(addr)
	if ip == nil {
		return strings.ToLower(addr)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
