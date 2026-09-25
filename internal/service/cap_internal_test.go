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
