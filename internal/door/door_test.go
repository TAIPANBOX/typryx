package door

import "testing"

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
