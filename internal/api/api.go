// Package api is typryx's HTTP surface: POST /v1/ask, POST /v1/outcome,
// GET /v1/templates, GET /healthz, and POST /mcp (internal/mcp.Server).
//
// The door (internal/door) runs before the body is parsed, on every route
// but /healthz, and identity comes only from the credential it resolves,
// never from a header the caller sent: X-Fuse-Agent-Id, X-Agent-Id and
// Agent-Passport are read by nothing here.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/TAIPANBOX/typryx/internal/door"
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
}

// NewMux builds the route table.
func NewMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/ask", s.withDoor(s.handleAsk))
	mux.HandleFunc("/v1/outcome", s.withDoor(s.handleOutcome))
	mux.HandleFunc("/v1/templates", s.withDoor(s.handleTemplates))
	mux.HandleFunc("/mcp", s.withDoor(s.handleMCP))
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, _ string) {
	writeJSON(w, status, map[string]string{"error": code})
}
