// Command speed times one decision three ways on the same items
// examples/calibration generates (seed 1): a typed one-token answer through
// typryx, a text judge, and a reasoning judge, each against its own endpoint.
// It prints median and p95 latency and accuracy per way. It is a measurement
// client of other services' public APIs, not part of typryx; it is exempted
// from scripts/one-way-out.sh for the same reason examples/calibration is.
//
//	go run ./examples/speed -mode typryx -url http://127.0.0.1:4320 -model LABEL
//	go run ./examples/speed -mode text -url https://api.openai.com/v1 -model M -keyfile F
//	go run ./examples/speed -mode reasoning -url https://api.openai.com/v1 -model M -keyfile F
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type result struct {
	Method, Model        string
	N, Correct, Unparsed int
	Lat                  []float64
	InTok, OutTok        int
}

func post(url, key string, body any) (map[string]any, float64, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		if strings.HasPrefix(key, "X-Typryx-Key:") {
			req.Header.Set("X-Typryx-Key", strings.TrimPrefix(key, "X-Typryx-Key:"))
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	t0 := time.Now()
	resp, err := (&http.Client{Timeout: 180 * time.Second}).Do(req)
	ms := float64(time.Since(t0).Microseconds()) / 1000
	if err != nil {
		return nil, ms, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, ms, fmt.Errorf("status %d: %.200s", resp.StatusCode, raw)
	}
	if resp.StatusCode >= 400 {
		return out, ms, fmt.Errorf("status %d: %.200s", resp.StatusCode, raw)
	}
	return out, ms, nil
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(p * float64(len(s)-1))
	return s[i]
}

const judgePrompt = "Task: %s\nFinal answer given: %s\n\nIs the final answer exactly correct? Work it out, then end with one line: VERDICT: TRUE or VERDICT: FALSE."

func main() {
	mode := flag.String("mode", "", "typryx | text | reasoning")
	url := flag.String("url", "", "endpoint base")
	model := flag.String("model", "", "model")
	keyFile := flag.String("keyfile", "", "bearer key file")
	flag.Parse()
	key := ""
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "key file unreadable")
			os.Exit(2)
		}
		key = strings.TrimSpace(string(b))
	}
	items := genItems(60, 1)
	r := result{Method: *mode, Model: *model}
	for _, it := range items {
		task := fmt.Sprintf("what is %d times %d", it.a, it.b)
		var said *bool
		var ms float64
		switch *mode {
		case "typryx":
			out, t, err := post(*url+"/v1/ask", "X-Typryx-Key:k1", map[string]any{"template": "eval.outcome_met", "state": map[string]string{"task": task, "final_answer": it.finalAnswer}})
			ms = t
			if err == nil {
				if p, ok := out["answer"].(float64); ok {
					v := p >= 0.5
					said = &v
				}
			}
		case "text", "reasoning":
			body := map[string]any{"model": *model, "messages": []map[string]string{{"role": "user", "content": fmt.Sprintf(judgePrompt, task, it.finalAnswer)}}}
			if *mode == "text" {
				body["temperature"] = 0
				body["max_tokens"] = 400
			} else {
				body["max_completion_tokens"] = 4000
			}
			out, t, err := post(*url+"/chat/completions", key, body)
			ms = t
			if err != nil {
				fmt.Fprintln(os.Stderr, "call error:", err)
				break
			}
			if u, ok := out["usage"].(map[string]any); ok {
				r.InTok += int(u["prompt_tokens"].(float64))
				r.OutTok += int(u["completion_tokens"].(float64))
			}
			ch := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			txt, _ := ch["content"].(string)
			up := strings.ToUpper(txt)
			i := strings.LastIndex(up, "VERDICT:")
			if i >= 0 {
				rest := up[i:]
				if strings.Contains(rest, "TRUE") {
					v := true
					said = &v
				} else if strings.Contains(rest, "FALSE") {
					v := false
					said = &v
				}
			}
		}
		r.N++
		r.Lat = append(r.Lat, ms)
		if said == nil {
			r.Unparsed++
		} else if *said == it.truth {
			r.Correct++
		}
	}
	var sum float64
	for _, x := range r.Lat {
		sum += x
	}
	fmt.Printf("%-9s %-24s n=%d acc=%.3f unparsed=%d p50=%.0fms p95=%.0fms mean=%.0fms total=%.1fs in_tok=%d out_tok=%d\n",
		r.Method, r.Model, r.N, float64(r.Correct)/float64(r.N), r.Unparsed, pct(r.Lat, 0.5), pct(r.Lat, 0.95), sum/float64(len(r.Lat)), sum/1000, r.InTok, r.OutTok)
}
