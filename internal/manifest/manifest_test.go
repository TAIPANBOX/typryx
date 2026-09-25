// The declaration in components.json is only worth reading if this repository
// proves it by RUNNING rather than by describing. Adapted from scopyx
// internal/manifest/manifest_test.go: build the real binary into a temp dir,
// start it several ways, and read what actually happened rather than what a
// document says should happen.
package manifest

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type requiredWhen struct {
	Backend string `json:"backend"`
	Why     string `json:"why"`
}

type envVar struct {
	Required     bool          `json:"required"`
	RequiredWhen *requiredWhen `json:"required_when"`
}

type openBind struct {
	ExitCode          int    `json:"exit_code"`
	EscapedByAKey     string `json:"escaped_by_a_key"`
	EscapedBySayingSo string `json:"escaped_by_saying_so"`
}

type component struct {
	Name    string `json:"name"`
	Class   string `json:"class"`
	Checked struct {
		Package                 string            `json:"package"`
		ListenDefault           string            `json:"listen_default"`
		HealthPath              string            `json:"health_path"`
		Env                     map[string]envVar `json:"env"`
		MissingRequiredExitCode int               `json:"missing_required_exit_code"`
		RefusesAnOpenBind       openBind          `json:"refuses_an_open_bind_without_a_credential"`
	} `json:"checked"`
}

type manifest struct {
	Schema     string      `json:"schema"`
	Repo       string      `json:"repo"`
	Components []component `json:"components"`
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func root(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func load(t *testing.T) (manifest, string) {
	t.Helper()
	r := root(t)
	b, err := os.ReadFile(filepath.Join(r, "components.json"))
	if err != nil {
		t.Fatalf("reading components.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing components.json: %v", err)
	}
	if len(m.Components) == 0 {
		t.Fatal("components.json declares no component, so every test here measured nothing")
	}
	return m, r
}

func service(t *testing.T, m manifest) component {
	t.Helper()
	for _, c := range m.Components {
		if c.Class == "service" {
			return c
		}
	}
	t.Fatal("components.json declares no service, so the running half measured nothing")
	return component{}
}

func build(t *testing.T, r, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "typryx")
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = r
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("splitting %q: %v", l.Addr().String(), err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

func startAndSee(t *testing.T, bin string, env []string) (stayedUp bool, code int, out string) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting it: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return false, exit.ExitCode(), buf.String()
		}
		if err == nil {
			return false, 0, buf.String()
		}
		t.Fatalf("waiting for it: %v", err)
	case <-time.After(2 * time.Second):
	}
	_ = cmd.Process.Kill()
	<-done
	return true, 0, buf.String()
}

// validTemplatesDir returns a directory holding one valid template, so tests
// that need TYPRYX_TEMPLATES to load successfully do not depend on
// examples/templates staying exactly as it is today.
func validTemplatesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "t.json"), []byte(`{
		"id": "t",
		"type": "noul",
		"instructions": "is it true",
		"fields": ["x"]
	}`), 0o644)
	if err != nil {
		t.Fatalf("writing a template fixture: %v", err)
	}
	return dir
}

func TestEveryBinaryThisRepositoryBuildsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)

	list := exec.Command("go", "list", "-f", `{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, "./...")
	list.Dir = r
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	built := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			built[line] = true
		}
	}
	if len(built) == 0 {
		t.Fatal("go list found no main package in this repository, so this measured nothing")
	}
	declared := map[string]bool{}
	for _, c := range m.Components {
		if c.Checked.Package == "" {
			t.Errorf("component %q declares no package", c.Name)
			continue
		}
		declared[c.Checked.Package] = true
	}
	for p := range built {
		if !declared[p] {
			t.Errorf("this repository builds %s and components.json does not declare it", p)
		}
	}
	for p := range declared {
		if !built[p] {
			t.Errorf("components.json declares %s and this repository does not build it", p)
		}
	}
}

// TestTheManifestMatchesWhatTheBinaryReads: every TYPRYX_ name in non-test
// source is declared, and every declared one is read. It reads STRING
// LITERALS rather than following os.Getenv calls, because a name a helper
// composes from parts would otherwise be invisible here and this file would
// report a set that is quietly short.
//
// cmd/typryx/connect.go is skipped: it prints configuration text for OTHER
// processes (a Claude Code MCP client, a curl invocation), and its default
// placeholder name, TYPRYX_KEY, names a variable in THAT client's own
// environment, not one typryx itself calls os.Getenv on. The regex cannot
// tell "a name this binary reads" from "a name this binary prints as an
// example for somebody else's shell", so the file that only ever does the
// second thing is named out rather than teaching components.json a
// variable nothing here reads (which TestConnectNeverPrintsAKey and the
// golden connect tests would not catch, since they are not about
// components.json at all).
func TestTheManifestMatchesWhatTheBinaryReads(t *testing.T) {
	m, r := load(t)

	name := regexp.MustCompile(`TYPRYX_[A-Z0-9_]+`)
	inSource := map[string]bool{}
	skip := filepath.Join("cmd", "typryx", "connect.go")
	err := filepath.Walk(r, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasSuffix(path, skip) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range name.FindAllString(string(b), -1) {
			if !strings.HasSuffix(n, "_") {
				inSource[n] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(inSource) == 0 {
		t.Fatal("no TYPRYX_ name found in any non-test .go file, so this measured nothing")
	}

	declared := map[string]bool{}
	for _, c := range m.Components {
		for k := range c.Checked.Env {
			declared[k] = true
		}
	}
	var missing, extra []string
	for n := range inSource {
		if !declared[n] {
			missing = append(missing, n)
		}
	}
	for n := range declared {
		if !inSource[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, n := range missing {
		t.Errorf("the code reads %s and components.json does not declare it", n)
	}
	for _, n := range extra {
		t.Errorf("components.json declares %s and no non-test source reads it", n)
	}
}

func TestTheDeclaredListenDefaultIsTheOneTheCodeUses(t *testing.T) {
	m, r := load(t)
	b, err := os.ReadFile(filepath.Join(r, "cmd", "typryx", "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	found := regexp.MustCompile(`defaultAddr\s*=\s*"([^"]*)"`).FindStringSubmatch(string(b))
	if found == nil {
		t.Fatal("main.go no longer defines defaultAddr, so this measured nothing")
	}
	if got := service(t, m).Checked.ListenDefault; got != found[1] {
		t.Errorf("components.json says the default listen address is %q; main.go says %q", got, found[1])
	}
}

// TestAWideBindWithNoCredentialRefusesToStart: the open-bind matrix, four
// rows. Every row sets TYPRYX_BACKEND and TYPRYX_TEMPLATES so the refusal
// under test is the bind refusal and nothing else; main.go checks required
// configuration before the open-bind refusal (documented in cmd/typryx/main.go),
// so this is the order under which the matrix has to hold.
func TestAWideBindWithNoCredentialRefusesToStart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	ob := c.Checked.RefusesAnOpenBind
	if ob.ExitCode == 0 || ob.EscapedByAKey == "" || ob.EscapedBySayingSo == "" {
		t.Fatal("components.json does not record the open-bind refusal in full, so this measured nothing")
	}
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	base := []string{"TYPRYX_BACKEND=stub", "TYPRYX_TEMPLATES=" + templatesDir}

	for _, row := range []struct {
		name      string
		addr      string
		extra     []string
		mustStart bool
	}{
		{"loopback with no credential", "127.0.0.1:", nil, true},
		{"open bind with no credential", "0.0.0.0:", nil, false},
		{"open bind, said so", "0.0.0.0:", []string{ob.EscapedBySayingSo + "=1"}, true},
		{"open bind with a key", "0.0.0.0:", []string{ob.EscapedByAKey + "=k1=agent://demo.example/tester"}, true},
	} {
		env := append(append([]string{}, base...), "TYPRYX_ADDR="+row.addr+freePort(t))
		env = append(env, row.extra...)
		up, code, out := startAndSee(t, bin, env)
		switch {
		case row.mustStart && !up:
			t.Errorf("%s: it exited %d and components.json says only an open bind with no "+
				"credential is refused\n%s", row.name, code, out)
		case !row.mustStart && up:
			t.Errorf("%s: it started. That is an unauthenticated typed-answer service on "+
				"whatever network this box is on, and the manifest claims it refuses.", row.name)
		case !row.mustStart && code != ob.ExitCode:
			t.Errorf("%s: it refused with exit %d; components.json says %d", row.name, code, ob.ExitCode)
		}
	}
}

func TestItRefusesWithoutEachRequiredVariable(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	want := c.Checked.MissingRequiredExitCode
	if want == 0 {
		t.Fatal("components.json declares no missing-required exit code, so this measured nothing")
	}

	var required []string
	for k, v := range c.Checked.Env {
		if v.Required {
			required = append(required, k)
		}
	}
	sort.Strings(required)
	if len(required) == 0 {
		t.Fatal("no variable is declared required, so this measured nothing")
	}

	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	working := map[string]string{
		"TYPRYX_BACKEND":   "stub",
		"TYPRYX_TEMPLATES": templatesDir,
		"TYPRYX_ADDR":      "127.0.0.1:" + freePort(t),
	}
	for _, missing := range required {
		var env []string
		for k, v := range working {
			if k != missing {
				env = append(env, k+"="+v)
			}
		}
		up, code, out := startAndSee(t, bin, env)
		if up {
			t.Errorf("without %s it started; components.json says it refuses", missing)
			continue
		}
		if code != want {
			t.Errorf("without %s it exited %d; components.json says %d\n%s", missing, code, want, out)
		}
		if !strings.Contains(out, missing) {
			t.Errorf("without %s the failure message does not name it:\n%s", missing, out)
		}
	}
}

// notBuiltBackendName is a backend name this build will never accept: unlike
// "jev" (built as of phase D) or "openai-logprobs" (phase C), this name
// never gains a case in cmd/typryx's loadConfig switch, so this test's own
// claim (that an unrecognised backend refuses, naming itself) does not go
// stale the day another backend is built.
const notBuiltBackendName = "not-a-real-backend"

// TestABackendNotBuiltYetRefusesToStart: an unrecognised TYPRYX_BACKEND (or
// anything but stub, openai-logprobs, and jev) refuses, naming the backend,
// at the config-error exit code.
func TestABackendNotBuiltYetRefusesToStart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	env := []string{
		"TYPRYX_BACKEND=" + notBuiltBackendName,
		"TYPRYX_TEMPLATES=" + templatesDir,
		"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
	}
	up, code, out := startAndSee(t, bin, env)
	if up {
		t.Fatalf("TYPRYX_BACKEND=%s started; that name is not a backend this build has", notBuiltBackendName)
	}
	if code != c.Checked.MissingRequiredExitCode {
		t.Errorf("exited %d; components.json's missing_required_exit_code is %d", code, c.Checked.MissingRequiredExitCode)
	}
	if !strings.Contains(out, notBuiltBackendName) || !strings.Contains(out, "not built yet") {
		t.Errorf("the refusal does not say backend %s is not built yet:\n%s", notBuiltBackendName, out)
	}
}

// --- required-when-chosen variables (the openai-logprobs backend) ----------

// openAIWorkingEnv is a full, valid set of the openai-logprobs backend's own
// variables, used as the "everything else is fine" baseline when testing one
// missing required-when-chosen variable at a time.
func openAIWorkingEnv() map[string]string {
	return map[string]string{
		"TYPRYX_OPENAI_URL":   "http://127.0.0.1:11434/v1",
		"TYPRYX_OPENAI_MODEL": "qwen2.5:3b",
	}
}

// jevWorkingEnv is the jev backend's own "everything else is fine" baseline,
// parameterized on a key file path since (unlike openai-logprobs, where none
// of its required_when variables is a file) jev's one required_when variable
// names a file that has to exist on disk for the positive control to work.
func jevWorkingEnv(keyFile string) map[string]string {
	return map[string]string{
		"TYPRYX_JEV_KEY_FILE": keyFile,
	}
}

// backendWorkingEnv maps a backend name (as a required_when note may name
// it) to that backend's own full working environment, so
// TestARequiredWhenChosenVariableRefusesToStartByNameWhenMissing can build a
// "just this one variable missing" case for whichever backend a required_when
// note names. Every function takes the jev key file path so one signature
// covers both backends; openai-logprobs simply ignores it.
var backendWorkingEnv = map[string]func(jevKeyFile string) map[string]string{
	"openai-logprobs": func(string) map[string]string { return openAIWorkingEnv() },
	"jev":             jevWorkingEnv,
}

// TestARequiredWhenChosenVariableRefusesToStartByNameWhenMissing walks every
// env var components.json declares with a required_when note (currently
// TYPRYX_OPENAI_URL and TYPRYX_OPENAI_MODEL for TYPRYX_BACKEND=openai-logprobs)
// and starts the real binary with that one variable missing and everything
// else that backend needs present, expecting the same missing-required exit
// code and message-names-the-variable behavior an unconditionally required
// variable gets.
func TestARequiredWhenChosenVariableRefusesToStartByNameWhenMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	jevKeyFile := filepath.Join(t.TempDir(), "jev-key.txt")
	if err := os.WriteFile(jevKeyFile, []byte("fake-jev-key-for-tests\n"), 0o600); err != nil {
		t.Fatalf("writing the jev key fixture: %v", err)
	}

	tested := 0
	for name, v := range c.Checked.Env {
		if v.RequiredWhen == nil {
			continue
		}
		tested++
		if v.RequiredWhen.Why == "" {
			t.Errorf("%s declares required_when with no why", name)
		}
		workingFn, ok := backendWorkingEnv[v.RequiredWhen.Backend]
		if !ok {
			t.Fatalf("%s declares required_when.backend %q, and this test has no working-env fixture for it",
				name, v.RequiredWhen.Backend)
		}
		env := []string{
			"TYPRYX_BACKEND=" + v.RequiredWhen.Backend,
			"TYPRYX_TEMPLATES=" + templatesDir,
			"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
		}
		for k, val := range workingFn(jevKeyFile) {
			if k == name {
				continue // the one variable under test, left out on purpose
			}
			env = append(env, k+"="+val)
		}
		up, code, out := startAndSee(t, bin, env)
		if up {
			t.Errorf("without %s it started; components.json's required_when says this must refuse", name)
			continue
		}
		if code != c.Checked.MissingRequiredExitCode {
			t.Errorf("without %s it exited %d; components.json says %d\n%s", name, code, c.Checked.MissingRequiredExitCode, out)
		}
		if !strings.Contains(out, name) {
			t.Errorf("without %s the failure message does not name it:\n%s", name, out)
		}
	}
	if tested == 0 {
		t.Fatal("no env var declares required_when, so this measured nothing")
	}
}

// TestOpenAILogprobsBackendStartsWithAFullConfiguration is the positive
// control for the test above: every required_when-chosen variable present
// together must actually be enough to start, not merely individually
// demanded.
func TestOpenAILogprobsBackendStartsWithAFullConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	env := []string{
		"TYPRYX_BACKEND=openai-logprobs",
		"TYPRYX_TEMPLATES=" + templatesDir,
		"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
		"TYPRYX_OPENAI_URL=http://127.0.0.1:11434/v1",
		"TYPRYX_OPENAI_MODEL=qwen2.5:3b",
	}
	up, code, out := startAndSee(t, bin, env)
	if !up {
		t.Fatalf("expected it to start with a full openai-logprobs configuration, exited %d\n%s", code, out)
	}
}

// TestTheOpenAIKeyFileContentsNeverAppearInOutput starts the real binary
// with a TYPRYX_OPENAI_KEY_FILE pointing at a fixture holding a marked
// secret, and checks the secret never reaches the process's own stdout or
// stderr, over and above internal/backend's own in-process
// TestTheKeyIsSentAsBearerAndNeverLogged.
func TestTheOpenAIKeyFileContentsNeverAppearInOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	const secret = "th3-0pen41-k3y-mus7-never-leak-9f8e7d"
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("writing the key fixture: %v", err)
	}
	env := []string{
		"TYPRYX_BACKEND=openai-logprobs",
		"TYPRYX_TEMPLATES=" + templatesDir,
		"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
		"TYPRYX_OPENAI_URL=http://127.0.0.1:11434/v1",
		"TYPRYX_OPENAI_MODEL=qwen2.5:3b",
		"TYPRYX_OPENAI_KEY_FILE=" + keyFile,
	}
	up, code, out := startAndSee(t, bin, env)
	if !up {
		t.Fatalf("expected it to start, exited %d\n%s", code, out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the key leaked into the process output: %s", out)
	}
}

// TestJevBackendStartsWithAFullConfiguration is the jev-backend positive
// control, the same shape as TestOpenAILogprobsBackendStartsWithAFullConfiguration:
// TYPRYX_JEV_KEY_FILE alone (jev's only required_when-chosen variable) must
// be enough, with TYPRYX_JEV_URL and TYPRYX_JEV_MODEL left at their defaults.
// Starting the process makes no outbound call by itself (the backend is only
// constructed, never asked anything at boot), so this needs no network and
// never reaches the real api.typesafe.ai.
func TestJevBackendStartsWithAFullConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	keyFile := filepath.Join(t.TempDir(), "jev-key.txt")
	if err := os.WriteFile(keyFile, []byte("fake-jev-key\n"), 0o600); err != nil {
		t.Fatalf("writing the jev key fixture: %v", err)
	}
	env := []string{
		"TYPRYX_BACKEND=jev",
		"TYPRYX_TEMPLATES=" + templatesDir,
		"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
		"TYPRYX_JEV_KEY_FILE=" + keyFile,
	}
	up, code, out := startAndSee(t, bin, env)
	if !up {
		t.Fatalf("expected it to start with a full jev configuration, exited %d\n%s", code, out)
	}
}

// TestTheJevKeyFileContentsNeverAppearInOutput starts the real binary with a
// TYPRYX_JEV_KEY_FILE pointing at a fixture holding a marked secret, and
// checks the secret never reaches the process's own stdout or stderr, over
// and above internal/backend's own in-process
// TestTheJevKeyIsSentAsBearerAndNeverLogged.
func TestTheJevKeyFileContentsNeverAppearInOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	m, r := load(t)
	c := service(t, m)
	bin := build(t, r, c.Checked.Package)
	templatesDir := validTemplatesDir(t)
	const secret = "th3-j3v-k3y-mus7-never-leak-2c4e6a"
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("writing the key fixture: %v", err)
	}
	env := []string{
		"TYPRYX_BACKEND=jev",
		"TYPRYX_TEMPLATES=" + templatesDir,
		"TYPRYX_ADDR=127.0.0.1:" + freePort(t),
		"TYPRYX_JEV_KEY_FILE=" + keyFile,
	}
	up, code, out := startAndSee(t, bin, env)
	if !up {
		t.Fatalf("expected it to start, exited %d\n%s", code, out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the key leaked into the process output: %s", out)
	}
}

// TestConnectGoNeverReadsAnEnvironmentVariable guards the exemption
// TestTheManifestMatchesWhatTheBinaryReads gives cmd/typryx/connect.go: that
// file is skipped from the TYPRYX_ scan on the premise that it only ever
// PRINTS example configuration for other processes and never calls
// os.Getenv itself. If it ever started reading a real environment variable,
// the exemption above would go on hiding it from the manifest gate; this
// fails loudly the day that premise stops being true.
func TestConnectGoNeverReadsAnEnvironmentVariable(t *testing.T) {
	_, r := load(t)
	path := filepath.Join(r, "cmd", "typryx", "connect.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if strings.Contains(string(b), "os.Getenv") {
		t.Fatalf("%s now calls os.Getenv; remove its exemption from TestTheManifestMatchesWhatTheBinaryReads and declare whatever it reads in components.json", path)
	}
}
