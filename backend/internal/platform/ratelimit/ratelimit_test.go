package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestBlocksAfterTheBurstAndTellsHowLongToWait(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 6, Burst: 3}, 100, c.now) // one token every 10 s
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d refused inside the burst", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok {
		t.Fatal("fourth request allowed")
	}
	if wait != 10*time.Second {
		t.Fatalf("wait = %v, want 10s", wait)
	}
	c.advance(9 * time.Second)
	if ok, wait := l.Allow("a"); ok || wait != time.Second {
		t.Fatalf("after 9s: ok=%v wait=%v, want refused, 1s", ok, wait)
	}
	c.advance(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("not allowed after the token refilled")
	}
}

func TestEachClientHasItsOwnBucket(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 1, Burst: 1}, 100, c.now)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a refused")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("a allowed twice")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("b was limited by a's requests")
	}
}

func TestRefillNeverExceedsTheBurst(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 60, Burst: 2}, 100, c.now)
	l.Allow("a")
	c.advance(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := l.Allow("a"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed %d after a long idle, want the burst of 2", allowed)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 1, Burst: 1}, 50, c.now)
	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
		if l.Len() > 50 {
			t.Fatalf("tracked %d keys, cap is 50", l.Len())
		}
	}
}

func TestEvictsTheLeastRecentlyUsedKeyFirst(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 1, Burst: 1}, 2, c.now)
	l.Allow("old")
	c.advance(time.Second)
	l.Allow("keep")
	c.advance(time.Second)
	l.Allow("old") // refused, but touches it: now "keep" is the least recent
	l.Allow("new") // evicts "keep"
	if ok, _ := l.Allow("old"); ok {
		t.Fatal("the busy key was evicted and got a fresh bucket")
	}
}

func TestIdleKeysAreDroppedOnceTheyWouldBeFullAgain(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 60, Burst: 5}, 1000, c.now) // full again after 5 s
	for i := 0; i < 20; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	c.advance(10 * time.Second)
	l.Allow("fresh")
	if l.Len() > 17 { // sweep removes up to 16 per call
		t.Fatalf("idle keys were not swept: %d tracked", l.Len())
	}
}

func TestClientKeyGroupsIPv6ByPrefix(t *testing.T) {
	if ClientKey("2001:db8:1:2::1") != ClientKey("2001:db8:1:2:ffff::9") {
		t.Error("two addresses in one /64 got different keys")
	}
	if ClientKey("2001:db8:1:2::1") == ClientKey("2001:db8:1:3::1") {
		t.Error("different /64 blocks share a key")
	}
	if ClientKey("203.0.113.7") == ClientKey("203.0.113.8") {
		t.Error("IPv4 addresses must stay separate")
	}
	if ClientKey("::ffff:203.0.113.7") != ClientKey("203.0.113.7") {
		t.Error("IPv4-mapped IPv6 must match the IPv4 key")
	}
}

// With many other idle keys the sweep (which forgets idle keys) is busy elsewhere, so this checks the refill cap
// itself and not the sweep.
func TestRefillCapHoldsEvenWhenTheKeyIsNotSwept(t *testing.T) {
	c := newClock()
	l := New(Rule{PerMinute: 60, Burst: 2}, 1000, c.now)
	for i := 0; i < 40; i++ {
		l.Allow(fmt.Sprintf("other%d", i))
	}
	l.Allow("a") // newest key: the sweep reaches the 16 oldest first
	c.advance(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := l.Allow("a"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("allowed %d after a long idle, want the burst of 2", allowed)
	}
}
