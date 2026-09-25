// Package door is typryx's credential check, shared by the HTTP surface and
// the MCP surface so both trust exactly the same identity.
//
// Adapted from scopyx internal/mcp/door.go: Keys/ParseKeys parse
// `key=agent://domain/name,key2,...`, credentials are compared in constant
// time, and RefuseOpenBind is the same open-bind refusal matrix. Identity
// comes only from the credential a caller presented, never from a header the
// caller wrote (invariant: identity comes from the credential, never a
// claim).
package door

import (
	"crypto/subtle"
	"fmt"
	"net"
	"strings"
)

// KeyHeader is the credential header every route but /healthz requires.
const KeyHeader = "X-Typryx-Key"

// Keys is the set of client credentials this door accepts. Empty means the
// door authenticates nobody, which is only safe on loopback (RefuseOpenBind
// enforces that).
type Keys struct {
	accepted []string
	identity map[string]string
}

// ParseKeys reads `key1,key2,...`, and optionally `key=agent://domain/name`.
//
// A credential given without an identity still authenticates; it just cannot
// be named as an agent. Every ask through it reaches the service with an
// empty AgentID, which the journal turns into "skipped and counted", never a
// fabricated identity.
func ParseKeys(raw string) Keys {
	var out []string
	ids := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, id, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, key)
		if found {
			if id = strings.TrimSpace(id); id != "" {
				ids[key] = id
			}
		}
	}
	return Keys{accepted: out, identity: ids}
}

// Identity is the agent a credential belongs to, or empty. Empty is a real
// answer, not an error.
func (k Keys) Identity(presented string) string {
	if !k.Allow(presented) {
		return ""
	}
	return k.identity[presented]
}

// Configured reports whether any credential is required.
func (k Keys) Configured() bool { return len(k.accepted) > 0 }

// Allow reports whether a presented credential is accepted, in constant
// time.
func (k Keys) Allow(presented string) bool {
	if !k.Configured() {
		return true
	}
	var ok bool
	for _, a := range k.accepted {
		if subtle.ConstantTimeCompare([]byte(a), []byte(presented)) == 1 {
			ok = true
		}
	}
	return ok
}

// IsLoopback reports whether a bind address is loopback.
func IsLoopback(addr string) bool {
	addr = strings.TrimSpace(addr)
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RefuseOpenBind returns the error typryx must refuse to start with, or an
// empty string to proceed. A non-loopback bind with no credential configured
// is an unauthenticated typed-answer service on whatever network the box is
// on, so this refuses rather than warns; the two escape hatches are
// deliberate: credentials, or the operator explicitly saying so.
func RefuseOpenBind(addr string, keys Keys, allowOpenBind bool) string {
	if keys.Configured() || allowOpenBind || IsLoopback(addr) {
		return ""
	}
	return fmt.Sprintf(
		"refusing to start: typryx is bound to %s, which is not loopback, and no client "+
			"credentials are configured (TYPRYX_KEYS is unset). Anything that reaches this "+
			"address could ask typed questions and spend this deployment's cap under nobody's "+
			"name. Set TYPRYX_KEYS=\"key1,key2\" to require a credential, bind to loopback "+
			"instead (TYPRYX_ADDR=127.0.0.1:4320, the default), or, if you have deliberately "+
			"decided to run it open, set TYPRYX_ALLOW_OPEN_BIND=1.", addr)
}

// TruthyEnv parses an opt-out variable the way the estate does: only `1` and
// `true` count, so a variable set to `0` or `no` means the opposite of what
// it would otherwise do.
func TruthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true":
		return true
	}
	return false
}
