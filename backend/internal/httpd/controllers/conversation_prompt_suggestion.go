package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

type promptSuggestionService interface {
	PromptSuggestion(context.Context, domain.SessionID, string) (string, error)
}

func (c *ConversationsController) promptSuggestion(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(promptSuggestionService)
	if !ok {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/sessions/{sessionId}/conversation/prompt-suggestion")
		return
	}
	var req ConversationPromptSuggestionRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if req.TurnID == "" || len(req.TurnID) > 128 || strings.TrimSpace(req.TurnID) != req.TurnID {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_TURN_ID_INVALID", "turnId must be a nonempty identifier of at most 128 bytes", nil)
		return
	}
	prompt, err := svc.PromptSuggestion(r.Context(), sessionID(r), req.TurnID)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ConversationPromptSuggestionResponse{TurnID: req.TurnID, Prompt: prompt})
}
