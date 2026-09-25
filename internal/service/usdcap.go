package service

import (
	"sync"
	"time"
)

// UsdCap is an optional daily USD spend cap, counted only against calls that
// reached a backend (the same "past the freeform gate, the template lookup
// and the egress filter" point the hourly Cap counts from).
//
// The window is the UTC calendar day, fixed, not a rolling 24 hours: the
// point, like the hourly Cap's, is a ceiling an operator can reason about
// from a log line ("today's spend"), not smooth pacing. It is held in
// process only and is lost on restart, exactly like the hourly Cap; README
// documents that beside the hourly cap's own restart behaviour.
type UsdCap struct {
	mu    sync.Mutex
	limit float64
	spent float64
	dayAt time.Time
	now   func() time.Time
}

// NewUsdCap builds a daily USD cap. limit must be a positive number of US
// dollars; a caller that means "no cap" simply never sets Service.UsdCap at
// all (cmd/typryx never constructs one for an unset or explicitly-0
// TYPRYX_MAX_USD_PER_DAY).
func NewUsdCap(limit float64) *UsdCap {
	return &UsdCap{limit: limit, now: time.Now, dayAt: utcDayStart(time.Now())}
}

// utcDayStart is the start (00:00:00 UTC) of the UTC calendar day t falls in.
func utcDayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// rollLocked resets today's spend to 0 the first time this cap is touched on
// a new UTC day. Called by both overLimit and add, under the lock, so a
// check made long after midnight with no intervening add still sees a fresh
// day, not yesterday's stale total.
func (c *UsdCap) rollLocked() {
	day := utcDayStart(c.now())
	if !day.Equal(c.dayAt) {
		c.dayAt = day
		c.spent = 0
	}
}

// overLimit reports whether today's spend has already reached the cap.
// ">=", deliberately: the call that would bring spend exactly to the limit
// is the one that reached it, and is the one refused, not the next call
// after it.
func (c *UsdCap) overLimit() bool {
	if c.limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked()
	return c.spent >= c.limit
}

// add adds cost to today's spend. A zero or negative cost (an unpriced
// backend always reports 0) is a no-op, so an unpriced deployment's daily
// total never moves even if a UsdCap were somehow configured over it
// (cmd/typryx refuses to start that combination in the first place; this is
// the belt under that suspender).
func (c *UsdCap) add(cost float64) {
	if c.limit <= 0 || cost <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked()
	c.spent += cost
}
