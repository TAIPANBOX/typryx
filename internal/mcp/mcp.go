// Package mcp is typryx's MCP surface: JSON-RPC 2.0 over POST /mcp, adapted
// from scopyx internal/mcp/server.go. It calls internal/service directly,
// the same package internal/api calls, which is what lets
// TestTheMCPToolAnswersTheSameAsTheHTTPRoute compare the two: there is one
// place ask logic lives and this is not it.
//
// The door itself already ran in internal/api before ServeMCP is reached;
// this package receives the resolved agent identity as a plain argument and
// never reads a credential or a claimed-identity header itself.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

const ProtocolVersion = "2025-06-18"

// Server is the JSON-RPC surface.
//
// Whether ask_freeform is listed is read live off Service.AllowFreeform,
// never a separate field here: a second copy of that flag is a second place
// it can disagree with the service that actually enforces it, and
// tools/list would then advertise something Ask itself would refuse.
type Server struct {
	Service *service.Service
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// ServeMCP answers one JSON-RPC request. agentID is the identity the door
// already resolved from the credential; ServeMCP trusts it and nothing else.
func (s *Server) ServeMCP(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "this endpoint takes JSON-RPC over POST", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeParse, "the request could not be parsed: " + err.Error()}})
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidRequest, `"jsonrpc" must be "2.0"`}})
		return
	}

	// A JSON-RPC notification is a request object with no "id" member (the
	// JSON-RPC 2.0 spec, and MCP 2025-06-18's Streamable HTTP transport
	// section, which says the transport MUST answer such a POST 202
	// Accepted with no body). This is keyed on the absent id, not on the
	// method name, so it holds for "notifications/initialized" and for any
	// other notification a future client sends. req.ID is nil only when the
	// field was absent from the JSON; an explicit "id":null is a (poorly
	// formed but present) id and is not treated as a notification.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "typryx", "version": Version},
		}})
	case "tools/list":
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.tools()}})
	case "tools/call":
		s.call(r, w, req, agentID)
	case "notifications/initialized":
		// Only reachable for a non-conforming call that names this method
		// but carries an id; the conforming, id-less case is caught above.
		// Answered the same way (202, no body) rather than as a request
		// with no result, since there is nothing to return either way.
		w.WriteHeader(http.StatusAccepted)
	default:
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeMethodNotFound, "unknown method " + req.Method}})
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) call(r *http.Request, w http.ResponseWriter, req rpcRequest, agentID string) {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidParams, "the params could not be read: " + err.Error()}})
		return
	}
	tool, ok := s.toolByName(p.Name)
	if !ok {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeMethodNotFound, "unknown tool " + p.Name}})
		return
	}
	args := map[string]any{}
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidParams, "the arguments could not be read: " + err.Error()}})
			return
		}
	}
	if err := validate(tool, args); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidParams, err.Error()}})
		return
	}

	result, isError := s.dispatch(r, tool.Name, args, agentID)
	b, err := json.Marshal(result)
	if err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidParams, "could not encode the result: " + err.Error()}})
		return
	}
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": result,
		"isError":           isError,
	}})
}

func (s *Server) dispatch(r *http.Request, name string, args map[string]any, agentID string) (any, bool) {
	switch name {
	case "ask":
		stateRaw, _ := json.Marshal(args["state"])
		req := service.AskRequest{Template: str(args["template"]), State: stateRaw, RunID: str(args["run_id"])}
		result, refusal := s.Service.Ask(r.Context(), service.Caller{AgentID: agentID, RunID: str(args["run_id"])}, req)
		if refusal != nil {
			return map[string]any{"error": refusal.Code, "message": refusal.Message}, true
		}
		return result, false
	case "ask_freeform":
		stateRaw, _ := json.Marshal(args["state"])
		criteriaRaw, _ := json.Marshal(args["criteria"])
		req := service.AskRequest{
			State: stateRaw, RunID: str(args["run_id"]),
			Question: &service.FreeformQuestion{
				Type: str(args["type"]), Instructions: str(args["instructions"]), Criteria: criteriaRaw,
			},
		}
		result, refusal := s.Service.Ask(r.Context(), service.Caller{AgentID: agentID, RunID: str(args["run_id"])}, req)
		if refusal != nil {
			return map[string]any{"error": refusal.Code, "message": refusal.Message}, true
		}
		return result, false
	case "list_questions":
		return listQuestions(s.Service), false
	}
	return map[string]any{"error": "unknown_tool"}, true
}

func listQuestions(svc *service.Service) any {
	type view struct {
		ID           string   `json:"id"`
		Version      string   `json:"version"`
		Type         string   `json:"type"`
		Fields       []string `json:"fields"`
		Instructions string   `json:"instructions"`
		Options      []string `json:"options,omitempty"`
		Levels       []string `json:"levels,omitempty"`
	}
	var out []view
	for _, t := range svc.Templates.List() {
		v := view{ID: t.ID, Version: t.Version(), Type: string(t.Type), Fields: t.Fields, Instructions: t.Instructions}
		switch t.Type {
		case template.TypeChoice:
			if opts, err := t.ChoiceOptions(); err == nil {
				for _, o := range opts {
					v.Options = append(v.Options, o.Name)
				}
			}
		case template.TypeScore:
			if levels, err := t.ScoreLevels(); err == nil {
				v.Levels = levels
			}
		}
		out = append(out, v)
	}
	return map[string]any{"templates": out}
}

// Tool is one MCP tool definition.
type Tool struct {
	Name        string
	Description string
	InputSchema Schema
}

// Schema is a minimal JSON Schema object: enough to describe typryx's own
// tools, not a general implementation.
type Schema struct {
	Type                 string
	Properties           map[string]Property
	Required             []string
	AdditionalProperties bool
}

type Property struct {
	Type string
	Enum []string
}

func (t Tool) MarshalJSON() ([]byte, error) {
	props := map[string]any{}
	for k, p := range t.InputSchema.Properties {
		prop := map[string]any{"type": p.Type}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[k] = prop
	}
	// t.InputSchema.Required is nil for a tool with no required fields
	// (list_questions), and encoding/json marshals a nil []string as JSON
	// null, not []. A strict client is entitled to expect an array here;
	// Claude Code 2.1.270 silently dropped typryx's whole tool list rather
	// than tolerate the null, so this is never left to marshal a nil slice.
	required := t.InputSchema.Required
	if required == nil {
		required = []string{}
	}
	return json.Marshal(map[string]any{
		"name":        t.Name,
		"description": t.Description,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": t.InputSchema.AdditionalProperties,
		},
	})
}

func (s *Server) tools() []Tool {
	tools := []Tool{
		{
			Name:        "ask",
			Description: "Ask a typed question from a registered template and get back a probability.",
			InputSchema: Schema{
				Properties: map[string]Property{
					"template": {Type: "string"},
					"state":    {Type: "object"},
					"run_id":   {Type: "string"},
				},
				Required: []string{"template", "state"},
			},
		},
		{
			Name:        "list_questions",
			Description: "List the templates this deployment can be asked.",
			InputSchema: Schema{Properties: map[string]Property{}},
		},
	}
	if s.Service.AllowFreeform {
		tools = append(tools, Tool{
			Name:        "ask_freeform",
			Description: "Ask a question that names no template. Only available when the operator switched freeform questions on.",
			InputSchema: Schema{
				Properties: map[string]Property{
					"type":         {Type: "string", Enum: []string{"choice", "score", "noul"}},
					"instructions": {Type: "string"},
					"criteria":     {Type: "object"},
					"state":        {Type: "object"},
					"run_id":       {Type: "string"},
				},
				Required: []string{"type", "instructions", "state"},
			},
		})
	}
	return tools
}

func (s *Server) toolByName(name string) (Tool, bool) {
	for _, t := range s.tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// validate enforces additionalProperties:false and required fields.
func validate(t Tool, args map[string]any) error {
	if !t.InputSchema.AdditionalProperties {
		var unknown []string
		for k := range args {
			if _, ok := t.InputSchema.Properties[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("%s does not accept %s", t.Name, strings.Join(unknown, ", "))
		}
	}
	for _, req := range t.InputSchema.Required {
		if _, ok := args[req]; !ok {
			return fmt.Errorf("%s requires %q", t.Name, req)
		}
	}
	for name, prop := range t.InputSchema.Properties {
		v, ok := args[name]
		if !ok || len(prop.Enum) == 0 {
			continue
		}
		got := str(v)
		allowed := false
		for _, e := range prop.Enum {
			if e == got {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("%s: %q must be one of %s, got %q", t.Name, name, strings.Join(prop.Enum, ", "), got)
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Version is stamped by the build.
var Version = "dev"
