// Copied from examples/calibration so both measure the same items for a seed.

package main

import (
	"math/rand"
	"strconv"
)

type item struct {
	a, b        int
	finalAnswer string
	truth       bool
}

func genItems(n int, seed int64) []item {
	r := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic example data, not a security context
	items := make([]item, n)
	for i := 0; i < n; i++ {
		a := 2 + r.Intn(18)
		b := 2 + r.Intn(18)
		correct := a * b
		wantCorrect := i%2 == 0
		var final int
		if wantCorrect {
			final = correct
		} else {
			final = plausibleWrongAnswer(correct, a, b, r)
		}
		items[i] = item{a: a, b: b, finalAnswer: strconv.Itoa(final), truth: wantCorrect}
	}
	return items
}

func plausibleWrongAnswer(correct, a, b int, r *rand.Rand) int {
	for {
		var candidate int
		switch r.Intn(3) {
		case 0:
			candidate = offByOneDigit(correct, r)
		case 1:
			candidate = correct + a
		default:
			candidate = correct + b
		}
		if candidate != correct {
			return candidate
		}
	}
}

func offByOneDigit(n int, r *rand.Rand) int {
	digits := []byte(strconv.Itoa(n))
	idx := r.Intn(len(digits))
	orig := digits[idx]
	for {
		d := byte('0') + byte(r.Intn(10)) // #nosec G115 -- r.Intn(10) is always 0..9, well within byte range
		if d != orig {
			digits[idx] = d
			break
		}
	}
	v, err := strconv.Atoi(string(digits))
	if err != nil {
		return n + 1
	}
	return v
}
