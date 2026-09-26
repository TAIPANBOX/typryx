package mcp

import "encoding/json"

// MetaKeyName is the params._meta field a "tools/call" may carry its
// credential under, when TYPRYX_ACCEPT_KEY_IN_META is set and the caller
// cannot set an HTTP header of its own. tokenfuse's MCP broker is the
// motivating case (cmd/typryx/connect.go's tokenfuse block): it forwards a
// brokered call to a named upstream with only a content-type header, but on
// "tools/call" it resolves any `{{secret:NAME}}` handle anywhere inside
// params, `_meta` included, from its own vault first.
const MetaKeyName = "typryx/key"

// envelope is the minimal JSON-RPC 2.0 shape NeedsNoCredential and
// ExtractMetaKey need. It is deliberately its own type rather than a reuse
// of rpcRequest: those callers already assume the door has resolved a
// credential, and both functions here run BEFORE that, so a body neither
// can even parse still has to be classified (as needing a credential),
// never treated as a decode failure to propagate.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// NeedsNoCredential reports whether body is a JSON-RPC message
// TYPRYX_ACCEPT_KEY_IN_META lets through with no credential at all:
// "initialize", "tools/list", or a JSON-RPC notification (a request object
// with no "id" member, the same absent-id rule ServeMCP itself answers 202
// for). All three carry tool schemas only and never reach a backend or name
// an agent; "tools/call" (ask, ask_freeform, list_questions dispatch through
// it) always needs one, and so does every other, unrecognized method. A
// body this cannot even parse needs a credential too: fail closed rather
// than guess at a message that could not be read.
func NeedsNoCredential(body []byte) bool {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	if len(env.ID) == 0 {
		return true
	}
	return env.Method == "initialize" || env.Method == "tools/list"
}

// ExtractMetaKey reports whether body is a "tools/call" whose
// params._meta[MetaKeyName] carries a credential, and returns body with
// that ONE entry removed: any other _meta field, and every other byte of
// the message, is left exactly as it was. ok is false, and body is
// returned completely unchanged, when the message is not a "tools/call",
// carries no params, no _meta, no MetaKeyName entry inside _meta, or that
// entry is not a JSON string; key is always "" in that case too, so a
// caller cannot end up using a value it failed to also strip.
//
// The caller (internal/api's door) calls this unconditionally, whether or
// not a header is also present and will end up being the one actually
// used: the point of removing it is that internal/mcp.Server, and anything
// it can reach (a log line, the journal, the ledger, an error message, a
// response), never sees this entry at all, used or not.
func ExtractMetaKey(body []byte) (key string, stripped []byte, ok bool) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Method != "tools/call" || len(env.Params) == 0 {
		return "", body, false
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return "", body, false
	}
	metaRaw, has := params["_meta"]
	if !has {
		return "", body, false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return "", body, false
	}
	keyRaw, has := meta[MetaKeyName]
	if !has {
		return "", body, false
	}
	var k string
	if err := json.Unmarshal(keyRaw, &k); err != nil {
		// Present but not a JSON string: not a usable credential. Left
		// completely untouched rather than guessing at a value to strip;
		// the request is simply judged to carry no meta credential at all.
		return "", body, false
	}

	// From here the entry is real: rebuild _meta without it, params without
	// _meta if that was its only entry, and the envelope with the rebuilt
	// params, never touching jsonrpc/id/method. Every value below came from
	// json.Unmarshal of a value this same call already validated, so
	// re-marshaling it cannot fail in practice; on the off chance one of
	// these does, the safest answer is still "not extracted, not stripped"
	// (the original, unmodified body), never "extracted but not stripped".
	delete(meta, MetaKeyName)
	if len(meta) == 0 {
		delete(params, "_meta")
	} else {
		mb, err := json.Marshal(meta)
		if err != nil {
			return "", body, false
		}
		params["_meta"] = mb
	}
	pb, err := json.Marshal(params)
	if err != nil {
		return "", body, false
	}
	env.Params = pb
	out, err := json.Marshal(env)
	if err != nil {
		return "", body, false
	}
	return k, out, true
}
