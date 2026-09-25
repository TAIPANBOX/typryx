// Package api is typryx's HTTP surface: POST /v1/ask, POST /v1/outcome,
// GET /v1/templates, GET /healthz, and POST /mcp (internal/mcp.Server).
//
// The door (internal/door) runs before the body is parsed, on every route
// but /healthz, and identity comes only from the credential it resolves,
// never from a header the caller sent: X-Fuse-Agent-Id, X-Agent-Id and
// Agent-Passport are read by nothing here. /v1/ask, /v1/outcome and
// /v1/templates never accept a key from the body, only from X-Typryx-Key,
// whatever AcceptKeyInMeta says: that flag names one JSON-RPC field of one
// method on /mcp alone, nothing on the /v1/* wire shape.
//
// This package imports internal/mcp for two pure functions
// (NeedsNoCredential, ExtractMetaKey) that read the JSON-RPC envelope
// TYPRYX_ACCEPT_KEY_IN_META needs to decide, before the door, whether a
// /mcp request needs a credential at all and where a "tools/call" may carry
// one; that import does not reintroduce the cycle MCPHandler exists to
// avoid, since internal/mcp still never imports this package, and
// dispatching the call itself still goes through that interface, never a
// direct call into internal/mcp.Server.
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/mcp"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

// MaxBodyBytes bounds every request body this surface reads.
const MaxBodyBytes = 1 << 20

// MCPHandler is implemented by internal/mcp.Server, kept as an interface here
// so this package does not import internal/mcp (internal/mcp imports this
// package's sibling, internal/service, directly; nothing needs a cycle).
type MCPHandler interface {
	ServeMCP(w http.ResponseWriter, r *http.Request, agentID string)
}

// Server is the HTTP surface.
type Server struct {
	Keys    door.Keys
	Service *service.Service
	MCP     MCPHandler

	// AcceptKeyInMeta is TYPRYX_ACCEPT_KEY_IN_META (door.TruthyEnv), off by
	// default. On, it changes /mcp alone: see handleMCPRoute.
	AcceptKeyInMeta bool
}

// NewMux builds the route table.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/ask", s.withDoor(s.handleAsk))
	mux.HandleFunc("/v1/outcome", s.withDoor(s.handleOutcome))
	mux.HandleFunc("/v1/templates", s.withDoor(s.handleTemplates))
	mux.HandleFunc("/mcp", s.handleMCPRoute)
	return mux
}

func (s *Server) withDoor(next func(w http.ResponseWriter, r *http.Request, agentID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(door.KeyHeader)
		if !s.Keys.Allow(key) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a client credential is required in "+door.KeyHeader)
			return
		}
		// Identity comes only from the credential the door resolved. Any
		// header a caller sent claiming an agent id is never read here or
		// anywhere downstream: internal/service.Caller is built from this
		// value alone.
		agentID := s.Keys.Identity(key)
		next(w, r, agentID)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	skipped, failed := s.Service.Journal.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"journal": map[string]any{
			"skipped_no_agent": skipped,
			"write_failed":     failed,
		},
		"ledger": map[string]any{
			"write_failed": s.Service.LedgerFailures(),
		},
	})
}

type askBody struct {
	Template string          `json:"template"`
	State    json.RawMessage `json:"state"`
	RunID    string          `json:"run_id"`
	Question *freeformBody   `json:"question"`
}

type freeformBody struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	var body askBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse the request body: "+err.Error())
		return
	}
	if !door.ValidRunID(body.RunID) {
		writeError(w, http.StatusBadRequest, "bad_run_id",
			"run_id must be at most 128 bytes with no control characters")
		return
	}
	req := service.AskRequest{Template: body.Template, State: body.State, RunID: body.RunID}
	if body.Question != nil {
		req.Question = &service.FreeformQuestion{
			Type: body.Question.Type, Instructions: body.Question.Instructions, Criteria: body.Question.Criteria,
		}
	}
	result, refusal := s.Service.Ask(r.Context(), service.Caller{AgentID: agentID, RunID: body.RunID}, req)
	if refusal != nil {
		writeError(w, refusal.HTTPStatus, refusal.Code, refusal.Message)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type outcomeBody struct {
	AnswerID string          `json:"answer_id"`
	Truth    json.RawMessage `json:"truth"`
	Source   string          `json:"source"`
}

func (s *Server) handleOutcome(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	var body outcomeBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse the request body: "+err.Error())
		return
	}
	result, refusal := s.Service.Outcome(service.Caller{AgentID: agentID}, service.OutcomeRequest{
		AnswerID: body.AnswerID, Truth: body.Truth, Source: body.Source,
	})
	if refusal != nil {
		writeError(w, refusal.HTTPStatus, refusal.Code, refusal.Message)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type templateView struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Type         string   `json:"type"`
	Fields       []string `json:"fields"`
	Instructions string   `json:"instructions"`
	Options      []string `json:"options,omitempty"`
	Levels       []string `json:"levels,omitempty"`
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	var views []templateView
	for _, t := range s.Service.Templates.List() {
		v := templateView{ID: t.ID, Version: t.Version(), Type: string(t.Type), Fields: t.Fields, Instructions: t.Instructions}
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
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": views})
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request, agentID string) {
	s.MCP.ServeMCP(w, r, agentID)
}

// handleMCPRoute is /mcp's own door. With AcceptKeyInMeta off, or for
// anything but POST, it is exactly withDoor(handleMCP): a credential in
// X-Typryx-Key, or a 401 shaped exactly like every other route's.
//
// With AcceptKeyInMeta on and a POST, the body has to be read before the
// door can be answered, because the decision itself depends on the JSON-RPC
// message inside it:
//
//   - "initialize", "tools/list", and a JSON-RPC notification need no
//     credential at all (mcp.NeedsNoCredential): none of them reach a
//     backend or name an agent, so nothing here is worth protecting.
//   - a "tools/call" with no X-Typryx-Key header may instead carry its
//     credential at params._meta["typryx/key"] (mcp.ExtractMetaKey),
//     resolved to an identity through the exact same door.Keys path a
//     header goes through; a header, when present, is always the one used.
//   - every other message (including one this cannot even parse) still
//     needs the header: fail closed.
//
// The _meta entry is stripped from the body unconditionally, whether or not
// it ends up used, before internal/mcp.Server (or anything it can reach: a
// log line, the journal, the ledger, an error message, a response) ever
// parses it.
func (s *Server) handleMCPRoute(w http.ResponseWriter, r *http.Request) {
	if !s.AcceptKeyInMeta || r.Method != http.MethodPost {
		s.withDoor(s.handleMCP)(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read the request body: "+err.Error())
		return
	}

	// Strip first, decide second: a message this turns out not to need a
	// credential for, or that carries a header instead, must not forward an
	// unused _meta credential downstream either.
	metaKey, strippedBody, hadMeta := mcp.ExtractMetaKey(body)
	if hadMeta {
		body = strippedBody
	}

	if mcp.NeedsNoCredential(body) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.handleMCP(w, r, "")
		return
	}

	key := r.Header.Get(door.KeyHeader)
	if key == "" {
		key = metaKey
	}
	if !s.Keys.Allow(key) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a client credential is required in "+door.KeyHeader)
		return
	}
	agentID := s.Keys.Identity(key)
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.handleMCP(w, r, agentID)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, _ string) {
	writeJSON(w, status, map[string]string{"error": code})
}
