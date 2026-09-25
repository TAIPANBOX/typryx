package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// connectTargets is the allow-list of clients `typryx connect` knows how to
// configure, spelled exactly as they appear on the command line.
var connectTargets = []string{"claude-code", "tokenfuse", "curl"}

const (
	defaultConnectURL = "http://127.0.0.1:4320"
	defaultKeyEnvName = "TYPRYX_KEY"
)

// connectCmd implements `typryx connect <target> [--url URL] [--key-env
// NAME]`. It prints ready-to-paste configuration for one client, nothing
// else, and exits 0. It never prints a real credential: the environment
// variable named by --key-env (TYPRYX_KEY by default) is never read here,
// only its NAME, so TYPRYX_KEYS set in this process's own environment
// cannot leak through this path even by accident.
func connectCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		sorted := sortedTargets()
		fmt.Fprintf(stderr, "typryx connect: a target is required. Known: %s\n", strings.Join(sorted, ", "))
		return 2
	}
	target := args[0]

	fs := flag.NewFlagSet("connect "+target, flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", defaultConnectURL, "the address typryx listens on")
	keyEnv := fs.String("key-env", defaultKeyEnvName, "the environment variable the client reads typryx's key from")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	base := strings.TrimSuffix(*url, "/")

	switch target {
	case "claude-code":
		fmt.Fprint(stdout, connectClaudeCode(base, *keyEnv))
	case "tokenfuse":
		fmt.Fprint(stdout, connectTokenfuse(base, *keyEnv))
	case "curl":
		fmt.Fprint(stdout, connectCurl(base, *keyEnv))
	default:
		sorted := sortedTargets()
		fmt.Fprintf(stderr, "typryx connect: unknown target %q. Known: %s\n", target, strings.Join(sorted, ", "))
		return 2
	}
	return 0
}

func sortedTargets() []string {
	sorted := append([]string(nil), connectTargets...)
	sort.Strings(sorted)
	return sorted
}

func connectClaudeCode(url, keyEnv string) string {
	return fmt.Sprintf(`# Claude Code, or any MCP client that speaks Streamable HTTP
claude mcp add --transport http typryx %[1]s/mcp --header "X-Typryx-Key: ${%[2]s}"

# equivalent .mcp.json
{"mcpServers":{"typryx":{"type":"http","url":"%[1]s/mcp","headers":{"X-Typryx-Key":"${%[2]s}"}}}}

# ${%[2]s} is a placeholder. Set it in the environment the client runs
# in; this command never prints the real value.
`, url, keyEnv)
}

func connectTokenfuse(url, keyEnv string) string {
	return fmt.Sprintf(`# tokenfuse MCP broker: typryx as a named upstream
TOKENFUSE_MCP_UPSTREAMS=typryx=%[1]s/mcp

# a caller picks it with:
X-Fuse-Mcp-Upstream: typryx

# the broker forwards a brokered call with only a content-type header: no
# credential, no agent identity (tokenfuse crates/gateway/src/mcpbroker.rs).
# On tools/call it does resolve {{secret:NAME}} handles anywhere inside
# params, _meta included, from its own vault, before forwarding:
TOKENFUSE_MCP_SECRETS=typryx_key=${%[2]s}
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
`, url, keyEnv)
}

func connectCurl(url, keyEnv string) string {
	return fmt.Sprintf(`# POST /v1/ask
curl -s -X POST %[1]s/v1/ask \
  -H "X-Typryx-Key: ${%[2]s}" -H 'Content-Type: application/json' \
  -d '{"template":"request.complexity","state":{"prompt":"summarize this doc"}}'

# POST /v1/outcome
curl -s -X POST %[1]s/v1/outcome \
  -H "X-Typryx-Key: ${%[2]s}" -H 'Content-Type: application/json' \
  -d '{"answer_id":"<the id from the ask above>","truth":"default","source":"human"}'

# ${%[2]s} is a placeholder. Export the real value before running
# these; this command never prints it.
`, url, keyEnv)
}
