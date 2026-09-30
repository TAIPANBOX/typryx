package manifest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startServing starts the real binary in dir and returns its base URL and a
// stop function that interrupts it, waits, and returns everything it logged.
func startServing(t *testing.T, bin, dir string, env []string) (base string, stop func() string) {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, env...), "TYPRYX_ADDR=127.0.0.1:"+port)
	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting it: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	stop = func() string {
		if !stopped {
			stopped = true
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
		return buf.String()
	}
	t.Cleanup(func() { stop() })
	base = "http://127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			return base, stop
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("it never answered /healthz:\n%s", stop())
	return "", nil
}

func postJSON(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func taskTemplatesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "t.json"), []byte(`{
		"id": "eval.outcome_met",
		"type": "noul",
		"instructions": "did it work",
		"fields": ["task"]
	}`), 0o644)
	if err != nil {
		t.Fatalf("writing a template fixture: %v", err)
	}
	return dir
}

// @test:TestTheTrainingLogIsOffByDefaultInTheRealBinary
//
// The real binary, started with no TYPRYX_TRAINING_DIR and asked a question
// that it answers: nothing is written anywhere it could have written, no
// directory is created, and the boot line says the log is off.
func TestTheTrainingLogIsOffByDefaultInTheRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	bin := build(t, r, service(t, m).Checked.Package)
	cwd := t.TempDir()
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	base, stop := startServing(t, bin, cwd, []string{
		"TYPRYX_BACKEND=stub", "TYPRYX_TEMPLATES=" + taskTemplatesDir(t), "TYPRYX_LEDGER_DIR=" + ledgerDir,
	})
	code, body := postJSON(t, base+"/v1/ask", `{"template":"eval.outcome_met","state":{"task":"t","secret":"OFF-SENTINEL"}}`)
	if code != http.StatusOK || strings.Contains(body, `"unanswered"`) {
		t.Fatalf("expected an answered ask, got %d %s", code, body)
	}
	logs := stop()
	if !strings.Contains(logs, "training_log=off") {
		t.Errorf("the boot line must say the training log is off:\n%s", logs)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("the working directory gained %d entries with the training log off", len(entries))
	}
	entries, err := os.ReadDir(ledgerDir)
	if err != nil {
		t.Fatalf("the ledger dir (this test's own control) is missing: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "training") {
			t.Errorf("a training file %s appeared next to the ledger with the training log off", e.Name())
		}
		b, _ := os.ReadFile(filepath.Join(ledgerDir, e.Name()))
		if strings.Contains(string(b), "OFF-SENTINEL") {
			t.Errorf("a held-back field reached %s", e.Name())
		}
	}
}

// @test:TestTheTrainingLogLoopRunsThroughTheRealBinary
//
// ask -> ledger -> truth via /v1/outcome -> training log -> export, on the
// built binary. The ask carries a field the template does not name; the
// exported row must hold the named field, the human truth as its label, and
// nothing the backend said.
func TestTheTrainingLogLoopRunsThroughTheRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	bin := build(t, r, service(t, m).Checked.Package)
	trainingDir := filepath.Join(t.TempDir(), "training")
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	base, stop := startServing(t, bin, t.TempDir(), []string{
		"TYPRYX_BACKEND=stub", "TYPRYX_TEMPLATES=" + taskTemplatesDir(t),
		"TYPRYX_LEDGER_DIR=" + ledgerDir, "TYPRYX_TRAINING_DIR=" + trainingDir,
	})

	// Two asks; only the first gets a truth.
	var ids []string
	var askedProbs []string
	for _, task := range []string{"first", "second"} {
		code, body := postJSON(t, base+"/v1/ask", `{"template":"eval.outcome_met","state":{"task":"`+task+`","secret":"LOOP-SENTINEL"}}`)
		if code != http.StatusOK {
			t.Fatalf("ask: %d %s", code, body)
		}
		var res struct {
			AnswerID      string             `json:"answer_id"`
			Probabilities map[string]float64 `json:"probabilities"`
		}
		if err := json.Unmarshal([]byte(body), &res); err != nil || res.AnswerID == "" || len(res.Probabilities) == 0 {
			t.Fatalf("the ask was not answered: %s", body)
		}
		ids = append(ids, res.AnswerID)
		b, _ := json.Marshal(res.Probabilities["true"])
		askedProbs = append(askedProbs, string(b))
	}
	if code, body := postJSON(t, base+"/v1/outcome", `{"answer_id":"`+ids[0]+`","truth":false,"source":"human"}`); code != http.StatusOK {
		t.Fatalf("outcome: %d %s", code, body)
	}
	logs := stop()
	if strings.Contains(logs, "LOOP-SENTINEL") || strings.Contains(logs, `"task"`) {
		t.Errorf("the state reached the process log:\n%s", logs)
	}
	if !strings.Contains(logs, "training_log="+trainingDir) {
		t.Errorf("the boot line must name the training directory:\n%s", logs)
	}

	// Permissions on what the process created.
	di, err := os.Stat(trainingDir)
	if err != nil {
		t.Fatalf("the training dir was not created: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("training dir is %o, want 0700", di.Mode().Perm())
	}
	file := filepath.Join(trainingDir, "training.ndjson")
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("training.ndjson: %v, mode %v, want 0600", err, fi)
	}

	raw, _ := os.ReadFile(file)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two training lines for two answered asks, got %d:\n%s", len(lines), raw)
	}
	if strings.Contains(string(raw), "LOOP-SENTINEL") || strings.Contains(string(raw), "secret") {
		t.Errorf("a held-back field reached training.ndjson:\n%s", raw)
	}
	if strings.Contains(string(raw), "probabilit") {
		t.Errorf("probabilities reached training.ndjson:\n%s", raw)
	}
	for _, p := range askedProbs {
		if strings.Contains(string(raw), p) {
			t.Errorf("the stub's probability %s reached training.ndjson:\n%s", p, raw)
		}
	}

	// Export: only the answer with a human truth, labelled with that truth.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "export", "--training", "--training-dir", trainingDir, "--ledger", ledgerDir)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("export: %v\n%s", err, stderr.String())
	}
	rows := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(rows) != 1 {
		t.Fatalf("expected one exported row, got %d:\n%s", len(rows), stdout.String())
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rows[0]), &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 5 || string(row["label"]) != "false" || string(row["state"]) != `{"task":"first"}` ||
		string(row["template"]) != `"eval.outcome_met"` || string(row["type"]) != `"noul"` {
		t.Errorf("unexpected row: %s", rows[0])
	}
	if !strings.Contains(stderr.String(), "eval.outcome_met exported=1 skipped_no_truth=1") {
		t.Errorf("counts on stderr: %s", stderr.String())
	}
}
