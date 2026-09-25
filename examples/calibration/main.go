// Command calibration is a reproducible measurement, not a library: it
// generates a deterministic set of arithmetic eval.outcome_met items, asks a
// running typryx to judge each one, tells typryx the real answer over
// POST /v1/outcome, and prints one summary line. Run it against a ledger
// directory, then run `typryx calibration` over the same directory to see
// whether the backend's stated probabilities can be trusted.
//
// It is a plain HTTP client of typryx's own API, the same shape as the curl
// examples in README.md, and is exempted from scripts/one-way-out.sh for
// exactly that reason (see that script's own comment): it runs outside the
// process the invariant governs and is not a backend.
//
// Example:
//
//	typryx calibration example: -url http://127.0.0.1:4320 -key k1 -n 60 -seed 1
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// item is one generated arithmetic ask: "what is A times B", a proposed
// final answer, and whether that answer actually achieves the task.
type item struct {
	a, b        int
	finalAnswer string
	truth       bool
}

// genItems deterministically generates n items for seed: A and B each in
// 2..19, the correct product for half the items and a plausible wrong
// answer (off by one digit, or off by A, or off by B) for the other half.
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

// plausibleWrongAnswer returns a value that differs from correct: one digit
// of correct changed, or correct shifted by a or by b, chosen at random.
// Guaranteed to differ from correct.
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

// offByOneDigit changes one random decimal digit of n to a different digit.
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

type askResponse struct {
	AnswerID   string `json:"answer_id"`
	Unanswered bool   `json:"unanswered"`
	Reason     string `json:"reason"`
	Answer     any    `json:"answer"`
}

func postJSON(client *http.Client, url, key string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Typryx-Key", key)
	}
	return client.Do(req)
}

func run(baseURL, key string, n int, seed int64, stdout, stderr *os.File) int {
	items := genItems(n, seed)
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimSuffix(baseURL, "/")

	var asked, answered, correct, outcomesRecorded int
	for _, it := range items {
		asked++
		state := map[string]string{
			"task":         fmt.Sprintf("what is %d times %d", it.a, it.b),
			"final_answer": it.finalAnswer,
		}
		resp, err := postJSON(client, base+"/v1/ask", key, map[string]any{
			"template": "eval.outcome_met",
			"state":    state,
		})
		if err != nil {
			fmt.Fprintf(stderr, "ask %d: %v\n", asked, err)
			continue
		}
		var ask askResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&ask)
		_ = resp.Body.Close()
		if decodeErr != nil {
			fmt.Fprintf(stderr, "ask %d: decoding response: %v\n", asked, decodeErr)
			continue
		}
		if ask.Unanswered || ask.AnswerID == "" {
			continue
		}
		answered++
		// deriveAnswer for a noul question is probabilities["true"]: a
		// number, not a boolean. "correct" here means typryx's own >0.5
		// threshold on that number agrees with the actual truth, purely for
		// this script's own summary line; typryx's calibration command
		// does the real, threshold-free scoring from the recorded
		// probabilities themselves.
		if p, ok := ask.Answer.(float64); ok && (p > 0.5) == it.truth {
			correct++
		}

		outResp, err := postJSON(client, base+"/v1/outcome", key, map[string]any{
			"answer_id": ask.AnswerID,
			"truth":     it.truth,
			"source":    "examples/calibration",
		})
		if err != nil {
			fmt.Fprintf(stderr, "outcome %d: %v\n", asked, err)
			continue
		}
		if outResp.StatusCode == http.StatusOK {
			outcomesRecorded++
		}
		_ = outResp.Body.Close()
	}

	acc := 0.0
	if answered > 0 {
		acc = float64(correct) / float64(answered)
	}
	fmt.Fprintf(stdout, "asked=%d answered=%d outcomes_recorded=%d threshold_accuracy=%.3f seed=%d\n",
		asked, answered, outcomesRecorded, acc, seed)
	if asked == 0 || outcomesRecorded == 0 {
		return 1
	}
	return 0
}

func main() {
	url := flag.String("url", "http://127.0.0.1:4320", "typryx base URL")
	key := flag.String("key", "", "X-Typryx-Key credential (empty: no credential is sent)")
	n := flag.Int("n", 60, "number of arithmetic items to generate")
	seed := flag.Int64("seed", 1, "random seed; the same seed always generates the same items")
	flag.Parse()

	os.Exit(run(*url, *key, *n, *seed, os.Stdout, os.Stderr))
}
