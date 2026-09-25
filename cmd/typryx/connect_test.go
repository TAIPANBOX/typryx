package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// @test:TestConnectPrintsReadyToPasteConfiguration
//
// Golden output for each target, with the default url and key-env name.
// Exact strings on purpose: this command's whole job is what a customer
// pastes somewhere else, so a silent reformat is a regression even when
// every substring check would still pass.
func TestConnectPrintsReadyToPasteConfiguration(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{
			target: "claude-code",
			want: `# Claude Code, or any MCP client that speaks Streamable HTTP
claude mcp add --transport http typryx http://127.0.0.1:4320/mcp --header "X-Typryx-Key: ${TYPRYX_KEY}"

# equivalent .mcp.json
{"mcpServers":{"typryx":{"type":"http","url":"http://127.0.0.1:4320/mcp","headers":{"X-Typryx-Key":"${TYPRYX_KEY}"}}}}

# ${TYPRYX_KEY} is a placeholder. Set it in the environment the client runs
# in; this command never prints the real value.
`,
		},
		{
			target: "tokenfuse",
			want: `# tokenfuse MCP broker: typryx as a named upstream
TOKENFUSE_MCP_UPSTREAMS=typryx=http://127.0.0.1:4320/mcp

# a caller picks it with:
X-Fuse-Mcp-Upstream: typryx

# the broker forwards a brokered call with only a content-type header: no
# credential, no agent identity (tokenfuse crates/gateway/src/mcpbroker.rs).
# On tools/call it does resolve {{secret:NAME}} handles anywhere inside
# params, _meta included, from its own vault, before forwarding:
TOKENFUSE_MCP_SECRETS=typryx_key=${TYPRYX_KEY}
TOKENFUSE_MCP_SECRET_SCOPES=typryx_key=agents:agent://demo.example/support-bot

# typryx opts in to reading that credential from a tools/call's own
# params._meta, since the broker sends no header of its own:
TYPRYX_ACCEPT_KEY_IN_META=1

# a client-side tools/call then carries the handle, never a real key:
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"eval.outcome_met","state":{"task":"..."}},"_meta":{"typryx/key":"{{secret:typryx_key}}"}}}

# with TYPRYX_ACCEPT_KEY_IN_META=1, initialize and tools/list need no
# credential (tool schemas only); ask, ask_freeform and list_questions still
# do, from the X-Typryx-Key header or params._meta. Without the flag every
# /mcp call needs the header, as before. tokenfuse itself is unchanged: typryx joins it
# through configuration alone.

# the OTHER direction: typryx's own spend, visible to a tokenfuse gateway's
# budget. tokenfuse is not changed for this; typryx joins it by pointing the
# openai-logprobs backend at the gateway (its own address, not the one
# above, which is where TYPRYX ITSELF listens) and opting in to two headers
# tokenfuse already reads. Off by default; on, x-fuse-run-id and
# x-fuse-agent-id carry the ask's own run id and agent, never sent to any
# other endpoint.
TYPRYX_BACKEND=openai-logprobs
TYPRYX_OPENAI_URL=http://<your-tokenfuse-gateway-host>:<port>/v1
TYPRYX_OPENAI_METER_HEADERS=1
`,
		},
		{
			target: "curl",
			want: `# POST /v1/ask
curl -s -X POST http://127.0.0.1:4320/v1/ask \
  -H "X-Typryx-Key: ${TYPRYX_KEY}" -H 'Content-Type: application/json' \
  -d '{"template":"request.complexity","state":{"prompt":"summarize this doc"}}'

# POST /v1/outcome
curl -s -X POST http://127.0.0.1:4320/v1/outcome \
  -H "X-Typryx-Key: ${TYPRYX_KEY}" -H 'Content-Type: application/json' \
  -d '{"answer_id":"<the id from the ask above>","truth":"default","source":"human"}'

# ${TYPRYX_KEY} is a placeholder. Export the real value before running
# these; this command never prints it.
`,
		},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := connectCmd([]string{c.target}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
			}
			if stdout.String() != c.want {
				t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", stdout.String(), c.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("expected nothing on stderr, got %q", stderr.String())
			}
		})
	}
}

// @test:TestConnectHonorsURLAndKeyEnvFlags
func TestConnectHonorsURLAndKeyEnvFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := connectCmd([]string{"curl", "--url", "https://typryx.example:4320/", "--key-env", "MY_TYPRYX_KEY"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "https://typryx.example:4320/v1/ask") {
		t.Errorf("expected the given --url to be used with no double slash, got %q", out)
	}
	if !strings.Contains(out, "${MY_TYPRYX_KEY}") {
		t.Errorf("expected the given --key-env name as the placeholder, got %q", out)
	}
	if strings.Contains(out, "${TYPRYX_KEY}") {
		t.Errorf("expected the default placeholder name to be gone once --key-env is set, got %q", out)
	}
}

// @test:TestConnectUnknownTargetExitsTwoAndListsValidOnes
func TestConnectUnknownTargetExitsTwoAndListsValidOnes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := connectCmd([]string{"not-a-real-client"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for an unknown target, got %d", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected nothing on stdout for a refused target, got %q", stdout.String())
	}
	for _, want := range []string{"claude-code", "tokenfuse", "curl"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("expected the refusal to list %q among the valid targets, got %q", want, stderr.String())
		}
	}
}

// @test:TestConnectWithNoTargetExitsTwo
func TestConnectWithNoTargetExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := connectCmd(nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit 2 with no target named, got %d", code)
	}
}

// @test:TestConnectNeverPrintsAKey
//
// TYPRYX_KEYS is set in the process environment, the way it would be on a
// real deployment, and every target's output is checked for the fake
// credential it carries. connectCmd never reads TYPRYX_KEYS at all, so this
// holds structurally; the test is here so that ever wiring the real value in
// by mistake would be caught rather than assumed away.
func TestConnectNeverPrintsAKey(t *testing.T) {
	const realKey = "th15-is-a-r34l-l00king-typryx-key-3xf1ltr4t3d"
	old, had := os.LookupEnv("TYPRYX_KEYS")
	os.Setenv("TYPRYX_KEYS", realKey+"=agent://demo.example/tester")
	t.Cleanup(func() {
		if had {
			os.Setenv("TYPRYX_KEYS", old)
		} else {
			os.Unsetenv("TYPRYX_KEYS")
		}
	})

	for _, target := range []string{"claude-code", "tokenfuse", "curl"} {
		var stdout, stderr bytes.Buffer
		connectCmd([]string{target}, &stdout, &stderr)
		if strings.Contains(stdout.String(), realKey) || strings.Contains(stderr.String(), realKey) {
			t.Errorf("%s: the real TYPRYX_KEYS value leaked into the output", target)
		}
	}
}

// @test:TestConnectIsWiredIntoMain
//
// Process-level: proves the real binary's main() actually dispatches to
// this subcommand, not only that the function works when called directly.
func TestConnectIsWiredIntoMain(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	bin := buildBinary(t)
	code, out := runBin(t, bin, nil, "connect", "curl")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	if !strings.Contains(out, "/v1/ask") {
		t.Errorf("expected the curl example in the real binary's output, got %s", out)
	}
}
