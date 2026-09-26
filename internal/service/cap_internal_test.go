package service

import (
	"testing"
	"time"
)

// TestCapWindowRollsOverWithAnInjectedClock is white-box (package service,
// not service_test) because Cap's clock seam (`now`) is unexported on
// purpose: real callers never need to inject one, only this test does.
func TestCapWindowRollsOverWithAnInjectedClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	c := NewCap(1)
	c.now = func() time.Time { return now }
	c.windowAt = now

	if !c.take() {
		t.Fatal("the first call within the limit should be admitted")
	}
	if c.take() {
		t.Fatal("the second call, over the limit, should be refused")
	}
	now = start.Add(time.Hour + time.Second)
	if !c.take() {
		t.Fatal("a call after the window rolled over should be admitted again")
	}
}

func TestRefusalErrorReturnsTheMessage(t *testing.T) {
	r := refusal("some_code", 400, "the reason is %s", "this")
	if r.Error() != "the reason is this" {
		t.Errorf("got %q", r.Error())
	}
}

// @test:TestUsdCapWindowRollsOverAtTheNextUTCDayWithAnInjectedClock
//
// White-box (package service, not service_test) for the same reason
// TestCapWindowRollsOverWithAnInjectedClock is: UsdCap's clock seam (`now`)
// is unexported on purpose, and only this test needs to inject one.
func TestUsdCapWindowRollsOverAtTheNextUTCDayWithAnInjectedClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	now := start
	c := NewUsdCap(1.0)
	c.now = func() time.Time { return now }
	c.dayAt = utcDayStart(now)

	c.add(1.0)
	if !c.overLimit() {
		t.Fatal("spend equal to the limit must be over it")
	}

	// Still the same UTC day, one second before midnight: the window must
	// NOT have rolled, and the cap must still be reached.
	now = start.Add(59 * time.Minute)
	if !c.overLimit() {
		t.Fatal("still the same UTC day: the cap must still be reached")
	}

	// Past midnight UTC: a new day, a fresh (empty) spend total.
	now = start.Add(2 * time.Hour)
	if c.overLimit() {
		t.Fatal("a new UTC day must roll the window and reset spend to 0")
	}
}

// @test:TestUsdCapAtZeroOrLessNeverRefusesOrAdds
func TestUsdCapAtZeroOrLessNeverRefusesOrAdds(t *testing.T) {
	c := NewUsdCap(0)
	c.add(1000)
	if c.overLimit() {
		t.Fatal("a cap of 0 (or less) must never refuse, however much was 'added'")
	}
}
