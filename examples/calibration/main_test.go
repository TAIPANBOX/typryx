package main

import (
	"strconv"
	"testing"
)

func TestGenItemsIsDeterministicForASeed(t *testing.T) {
	a := genItems(60, 1)
	b := genItems(60, 1)
	if len(a) != 60 || len(b) != 60 {
		t.Fatalf("expected 60 items each, got %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("item %d differs between two runs of the same seed: %+v vs %+v", i, a[i], b[i])
		}
	}
	c := genItems(60, 2)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("expected a different seed to produce a different sequence")
	}
}

func TestGenItemsHalfCorrectHalfWrong(t *testing.T) {
	items := genItems(60, 1)
	correct, wrong := 0, 0
	for _, it := range items {
		want := strconv.Itoa(it.a * it.b)
		if it.truth {
			correct++
			if it.finalAnswer != want {
				t.Errorf("item claims truth=true but final_answer %q != correct product %q", it.finalAnswer, want)
			}
		} else {
			wrong++
			if it.finalAnswer == want {
				t.Errorf("item claims truth=false but final_answer %q equals the correct product", it.finalAnswer)
			}
		}
	}
	if correct != 30 || wrong != 30 {
		t.Errorf("expected 30 correct and 30 wrong out of 60, got %d correct, %d wrong", correct, wrong)
	}
}

func TestGenItemsFactorsInRange(t *testing.T) {
	for _, it := range genItems(200, 7) {
		if it.a < 2 || it.a > 19 || it.b < 2 || it.b > 19 {
			t.Fatalf("factor out of [2,19]: a=%d b=%d", it.a, it.b)
		}
	}
}
