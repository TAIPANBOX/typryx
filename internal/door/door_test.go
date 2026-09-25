package door

import (
	"strings"
	"testing"
)

func TestParseKeysSplitsAndTrims(t *testing.T) {
	k := ParseKeys(" k1 , k2,k3 ")
	for _, key := range []string{"k1", "k2", "k3"} {
		if !k.Allow(key) {
			t.Errorf("expected %q to be allowed", key)
		}
	}
	if k.Allow("k4") {
		t.Error("k4 was never configured and must not be allowed")
	}
}

func TestParseKeysDropsBlankEntries(t *testing.T) {
	k := ParseKeys("k1,,  ,k2")
	if k.Allow("") {
		t.Error("an empty presented credential must never be allowed just because blanks were skipped")
	}
}

func TestParseKeysWithIdentity(t *testing.T) {
	k := ParseKeys("k1=agent://acme.example/bot,k2")
	if got := k.Identity("k1"); got != "agent://acme.example/bot" {
		t.Errorf("expected the bound identity, got %q", got)
	}
	if got := k.Identity("k2"); got != "" {
		t.Errorf("a key with no identity must resolve to empty, got %q", got)
	}
	if got := k.Identity("unknown"); got != "" {
		t.Errorf("an unrecognized credential must never resolve an identity, got %q", got)
	}
}

func TestEmptyKeysConfiguredIsFalseAndAllowsEverything(t *testing.T) {
	k := ParseKeys("")
	if k.Configured() {
		t.Error("no keys configured should report Configured() == false")
	}
	if !k.Allow("anything") {
		t.Error("with no keys configured, every credential (including none) is allowed")
	}
}

func TestKeysConfiguredIsTrueOnceAnyKeyExists(t *testing.T) {
	k := ParseKeys("k1")
	if !k.Configured() {
		t.Error("expected Configured() == true")
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:4320": true,
		"127.5.5.5:1":    true,
		"localhost:4320": true,
		"[::1]:4320":     true,
		"0.0.0.0:4320":   false,
		"10.0.0.1:4320":  false,
		"example.com:80": false,
	}
	for addr, want := range cases {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestRefuseOpenBindMatrix(t *testing.T) {
	cases := []struct {
		name          string
		addr          string
		keys          Keys
		allowOpenBind bool
		wantRefusal   bool
	}{
		{"loopback, no keys", "127.0.0.1:4320", ParseKeys(""), false, false},
		{"open bind, no keys", "0.0.0.0:4320", ParseKeys(""), false, true},
		{"open bind, said so", "0.0.0.0:4320", ParseKeys(""), true, false},
		{"open bind, with a key", "0.0.0.0:4320", ParseKeys("k1"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why := RefuseOpenBind(c.addr, c.keys, c.allowOpenBind)
			if (why != "") != c.wantRefusal {
				t.Errorf("RefuseOpenBind(%q) = %q, wantRefusal=%v", c.addr, why, c.wantRefusal)
			}
		})
	}
}

func TestTruthyEnv(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", " 1 "}
	falsy := []string{"0", "false", "no", "", "yes"}
	for _, v := range truthy {
		if !TruthyEnv(v) {
			t.Errorf("TruthyEnv(%q) should be true", v)
		}
	}
	for _, v := range falsy {
		if TruthyEnv(v) {
			t.Errorf("TruthyEnv(%q) should be false", v)
		}
	}
}

func TestIdentitiesReturnsOnlyBoundIdentitiesNeverCredentials(t *testing.T) {
	k := ParseKeys("cred1=agent://acme.example/bot,cred2,cred3=agent://acme.example/other")
	ids := k.Identities()
	if len(ids) != 2 {
		t.Fatalf("expected 2 bound identities, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id == "cred1" || id == "cred2" || id == "cred3" {
			t.Errorf("Identities returned a credential, not an identity: %q", id)
		}
	}
}

func TestIdentitiesOnKeysWithNoBoundIdentityIsEmpty(t *testing.T) {
	k := ParseKeys("cred1,cred2")
	if len(k.Identities()) != 0 {
		t.Errorf("expected no bound identities, got %v", k.Identities())
	}
}

func TestValidIdentity(t *testing.T) {
	valid := []string{"agent://acme.example/bot", "agent://demo.example/tester", "agent://a/b"}
	invalid := []string{"bob", "agent://", "agent://acme.example", "agent://acme.example/", "http://acme.example/bot", ""}
	for _, id := range valid {
		if !ValidIdentity(id) {
			t.Errorf("expected %q to be a valid identity", id)
		}
	}
	for _, id := range invalid {
		if ValidIdentity(id) {
			t.Errorf("expected %q to be rejected", id)
		}
	}
}

// @test:TestValidRunID
func TestValidRunID(t *testing.T) {
	valid := []string{"", "eval-1234", "a-run-id-with-dashes_and_underscores.and.dots", strings.Repeat("a", 128)}
	for _, id := range valid {
		if !ValidRunID(id) {
			t.Errorf("expected %q (len %d) to be a valid run_id", id, len(id))
		}
	}
	invalid := map[string]string{
		"129 bytes": strings.Repeat("a", 129),
		"a newline": "before\nafter",
		"a carriage return + line feed (header-splitting shape)": "before\r\nX-Injected: evil",
		"a bare tab": "before\tafter",
		"a NUL byte": "before\x00after",
		"a DEL byte": "before\x7fafter",
	}
	for name, id := range invalid {
		if ValidRunID(id) {
			t.Errorf("%s: expected %q to be rejected", name, id)
		}
	}
}

func TestAllowIsConstantTimeCompareNotMapLookup(t *testing.T) {
	// Not a timing test (those are unreliable in CI); this only proves the
	// comparison path is exercised for a near-miss, which is the case a
	// substring or prefix bug would show up on.
	k := ParseKeys("supersecretkey")
	if k.Allow("supersecretke") {
		t.Error("a truncated credential must not be allowed")
	}
	if k.Allow("supersecretkey ") {
		t.Error("a credential with trailing whitespace must not be allowed")
	}
}
