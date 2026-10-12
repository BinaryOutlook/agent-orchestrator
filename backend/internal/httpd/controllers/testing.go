package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

// TestingService is the controller-facing contract for `ao testing` verbs.
type TestingService interface {
	Start(ctx context.Context, req litmus.StartRequest) (litmus.Started, error)
	Intake(ctx context.Context, sessionID, prURL string) (litmus.Run, error)
	Prepare(ctx context.Context, sessionID string) (litmus.Run, error)
	Launch(ctx context.Context, sessionID, leg string) (litmus.Run, error)
	Status(ctx context.Context, sessionID string) (litmus.RunStatus, error)
	CheckAdd(ctx context.Context, sessionID, name, expect, leg string) (litmus.Check, error)
	CheckStart(ctx context.Context, sessionID, id string) (litmus.Check, error)
	CheckEnd(ctx context.Context, sessionID, id string, result litmus.CheckState, note string) (litmus.Check, error)
	Compare(ctx context.Context, sessionID, checkID string) (map[string]any, error)
	Submit(ctx context.Context, sessionID, summary, skipBase string) (litmus.Verdict, error)
	SubmitBlocked(ctx context.Context, sessionID, reason string) (litmus.Verdict, error)
	Cleanup(ctx context.Context, sessionID string) (litmus.Run, error)
	Drive(ctx context.Context, sessionID, action string, args []string) (map[string]any, error)
	Test(ctx context.Context, sessionID, leg, dir string, argv []string, withTestsFrom string) (litmus.TestResult, error)
	RecipeValidate(ctx context.Context, path string) (litmus.Recipe, error)
	Prune(ctx context.Context, keepRuns, keepCheckouts int) (litmus.PruneReport, error)
	PruneDryRun(ctx context.Context, keepRuns, keepCheckouts int) (litmus.PruneReport, error)
}

// TestingController owns the /testing routes. The router mounts it only when a
// service is wired.
type TestingController struct{ Svc TestingService }

// Register mounts the testing routes on r.
func (c *TestingController) Register(r chi.Router) {
	s := c.Svc
	r.Get("/testing/status", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Status(r.Context(), strings.TrimSpace(r.URL.Query().Get("sessionId")))
		writeTesting(w, r, out, err)
	})
	r.Post("/testing/start", testingPost(func(r *http.Request, in TestingRequest) (litmus.Started, error) {
		return s.Start(r.Context(), litmus.StartRequest{ProjectID: in.ProjectID, Harness: in.Harness, PRURL: in.PRURL, NoComment: in.NoComment, NoEvidence: in.NoEvidence})
	}))
	r.Post("/testing/intake", testingPost(func(r *http.Request, in TestingRequest) (litmus.Run, error) {
		return s.Intake(r.Context(), in.SessionID, in.PRURL)
	}))
	r.Post("/testing/prepare", testingPost(func(r *http.Request, in TestingRequest) (litmus.Run, error) {
		return s.Prepare(r.Context(), in.SessionID)
	}))
	r.Post("/testing/launch", testingPost(func(r *http.Request, in TestingRequest) (litmus.Run, error) {
		return s.Launch(r.Context(), in.SessionID, in.Leg)
	}))
	r.Post("/testing/checks", testingPost(func(r *http.Request, in TestingRequest) (litmus.Check, error) {
		return s.CheckAdd(r.Context(), in.SessionID, in.Name, in.Expect, in.Leg)
	}))
	r.Post("/testing/checks/{id}/start", testingPost(func(r *http.Request, in TestingRequest) (litmus.Check, error) {
		return s.CheckStart(r.Context(), in.SessionID, chi.URLParam(r, "id"))
	}))
	r.Post("/testing/checks/{id}/end", testingPost(func(r *http.Request, in TestingRequest) (litmus.Check, error) {
		return s.CheckEnd(r.Context(), in.SessionID, chi.URLParam(r, "id"), litmus.CheckState(in.Result), in.Note)
	}))
	r.Post("/testing/checks/{id}/compare", testingPost(func(r *http.Request, in TestingRequest) (map[string]any, error) {
		return s.Compare(r.Context(), in.SessionID, chi.URLParam(r, "id"))
	}))
	r.Post("/testing/submit", testingPost(func(r *http.Request, in TestingRequest) (litmus.Verdict, error) {
		if in.Blocked {
			return s.SubmitBlocked(r.Context(), in.SessionID, in.Reason)
		}
		return s.Submit(r.Context(), in.SessionID, in.Summary, in.SkipBase)
	}))
	r.Post("/testing/cleanup", testingPost(func(r *http.Request, in TestingRequest) (litmus.Run, error) {
		return s.Cleanup(r.Context(), in.SessionID)
	}))
	r.Post("/testing/drive", testingPost(func(r *http.Request, in TestingRequest) (map[string]any, error) {
		return s.Drive(r.Context(), in.SessionID, strings.ToLower(strings.TrimSpace(in.Action)), in.Args)
	}))
	r.Post("/testing/test", testingPost(func(r *http.Request, in TestingRequest) (litmus.TestResult, error) {
		return s.Test(r.Context(), in.SessionID, in.Leg, in.Dir, in.Args, in.WithTestsFrom)
	}))
	r.Post("/testing/recipe/validate", testingPost(func(r *http.Request, in TestingRequest) (litmus.Recipe, error) {
		return s.RecipeValidate(r.Context(), in.Path)
	}))
	r.Post("/testing/prune", testingPost(func(r *http.Request, in TestingRequest) (litmus.PruneReport, error) {
		keepRuns, keepCheckouts := litmus.DefaultKeepRuns, litmus.DefaultKeepCheckouts
		if in.KeepRuns != nil {
			keepRuns = *in.KeepRuns
		}
		if in.KeepCheckouts != nil {
			keepCheckouts = *in.KeepCheckouts
		}
		if in.DryRun {
			return s.PruneDryRun(r.Context(), keepRuns, keepCheckouts)
		}
		return s.Prune(r.Context(), keepRuns, keepCheckouts)
	}))
}

// testingPost decodes a strict JSON body, calls the verb, and renders the result or error.
func testingPost[T any](call func(*http.Request, TestingRequest) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in TestingRequest
		if err := decodeJSONStrict(r, &in); err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
			return
		}
		in.SessionID = strings.TrimSpace(in.SessionID) // the status route trims too
		out, err := call(r, in)
		writeTesting(w, r, out, err)
	}
}

func writeTesting(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}
