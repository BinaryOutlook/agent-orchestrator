package chat_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func settledSuggestionTurn(t *testing.T, h *harness) string {
	t.Helper()
	turnID := completeTurn(t, h, "Fix the error", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted && h.ctrl.State() == ports.ChatControllerReady
	})
	return turnID
}

func TestPromptSuggestionUsesIsolatedBoundedContextWithoutChangingHistory(t *testing.T) {
	h := newHarness(t)
	turnID := settledSuggestionTurn(t, h)
	before, err := h.svc.Snapshot(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetPromptSuggestionRunner(func(ctx context.Context, id domain.SessionID, systemPrompt, prompt string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("suggestion runner has no 30-second deadline")
		}
		if id != testSession || !strings.Contains(systemPrompt, "Do not answer the prompt, use tools") ||
			prompt != systemPrompt+"\n\nRecent transcript:\nuser: Fix the error\n\nassistant: reply to Fix the error\n\n" {
			t.Fatalf("runner context: id=%s system=%q prompt=%q", id, systemPrompt, prompt)
		}
		return "\nAdd a regression test.\n", nil
	})
	prompt, err := h.svc.PromptSuggestion(context.Background(), testSession, turnID)
	if err != nil || prompt != "Add a regression test." {
		t.Fatalf("suggestion = %q, %v", prompt, err)
	}
	after, err := h.svc.Snapshot(context.Background(), testSession)
	if err != nil || len(after.Turns) != len(before.Turns) || len(after.Messages) != len(before.Messages) ||
		after.Conversation.LatestSequence != before.Conversation.LatestSequence || len(h.conv.sentMessages()) != 1 {
		t.Fatalf("suggestion changed the conversation: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestPromptSuggestionRejectsUnavailableOrStaleContext(t *testing.T) {
	for _, scenario := range []string{"missing", "terminal", "terminated", "old", "running", "queued", "rolled-back", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarnessWithConversation(t, newHistoryRecorder())
			turnID := settledSuggestionTurn(t, h)
			h.svc.SetPromptSuggestionRunner(func(context.Context, domain.SessionID, string, string) (string, error) {
				t.Fatal("provider was called for an unavailable suggestion")
				return "", nil
			})
			id := testSession
			want := chatsvc.ErrPromptSuggestionStale
			switch scenario {
			case "missing":
				id, want = "missing-session", ports.ErrSessionNotFound
			case "terminal":
				changed, err := h.st.CommitSessionControllerEpoch(context.Background(), testSession, domain.SessionModeChat, domain.SessionModeTUI, "thread-1", h.now())
				if err != nil || !changed {
					t.Fatalf("commit TUI mode: changed=%v err=%v", changed, err)
				}
				want = chatsvc.ErrNotChatMode
			case "terminated":
				record, _, err := h.st.GetSession(context.Background(), testSession)
				if err != nil {
					t.Fatal(err)
				}
				record.IsTerminated = true
				if err := h.st.UpdateSession(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			case "old":
				turnID = "older-turn"
			case "running", "queued":
				if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "new work", Origin: domain.MessageOriginHuman}); err != nil {
					t.Fatal(err)
				}
				if scenario == "queued" {
					if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "queued work", Origin: domain.MessageOriginHuman}); err != nil {
						t.Fatal(err)
					}
				}
			case "rolled-back":
				if _, err := h.svc.Rollback(context.Background(), testSession, turnID); err != nil {
					t.Fatal(err)
				}
			case "failed":
				if err := h.st.SettleTurnByID(context.Background(), turnID, domain.TurnStateFailed, "failed", h.now()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.svc.PromptSuggestion(context.Background(), id, turnID); !errors.Is(err, want) {
				t.Fatalf("suggestion error = %v, want %v", err, want)
			}
		})
	}
}

func TestPromptSuggestionDiscardsResultWhenConversationChanges(t *testing.T) {
	for _, scenario := range []string{"send", "rollback", "termination", "branch", "revised-answer"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarnessWithConversation(t, newHistoryRecorder())
			turnID := settledSuggestionTurn(t, h)
			h.svc.SetPromptSuggestionRunner(func(ctx context.Context, _ domain.SessionID, _, _ string) (string, error) {
				switch scenario {
				case "send":
					_, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "I already chose", Origin: domain.MessageOriginHuman})
					if err != nil {
						t.Fatal(err)
					}
				case "rollback":
					if _, err := h.svc.Rollback(ctx, testSession, turnID); err != nil {
						t.Fatal(err)
					}
				case "termination":
					record, _, err := h.st.GetSession(ctx, testSession)
					if err != nil {
						t.Fatal(err)
					}
					record.IsTerminated = true
					if err := h.st.UpdateSession(ctx, record); err != nil {
						t.Fatal(err)
					}
				case "branch":
					snapshot, err := h.svc.Snapshot(ctx, testSession)
					if err != nil {
						t.Fatal(err)
					}
					branch := domain.ConversationBranch{
						ID: "suggestion-sibling", ConversationID: snapshot.Conversation.ID, SessionID: testSession,
						ProviderConversationID: "thread-sibling", ParentBranchID: snapshot.ActiveBranch.ID,
						ForkAfterTurnID: turnID, ForkAfterSequence: snapshot.Conversation.LatestSequence,
					}
					if err := h.st.CreateConversationBranch(ctx, branch, h.now()); err != nil {
						t.Fatal(err)
					}
					if err := h.st.ActivateConversationBranch(ctx, testSession, branch.ConversationID, branch.ID, branch.ProviderConversationID, h.ctrl.Generation(), h.now()); err != nil {
						t.Fatal(err)
					}
				case "revised-answer":
					if err := h.st.SettleAssistantMessage(ctx, h.ctrl.ConversationID(), "msg-provider-turn-1", "provider-turn-1", "A different answer", "replacement-message", h.now()); err != nil {
						t.Fatal(err)
					}
				}
				return "Run the tests.", nil
			})
			if prompt, err := h.svc.PromptSuggestion(context.Background(), testSession, turnID); prompt != "" || !errors.Is(err, chatsvc.ErrPromptSuggestionStale) {
				t.Fatalf("stale suggestion = %q, %v", prompt, err)
			}
		})
	}
}

func TestPromptSuggestionSurvivesActivityOnlyUpdates(t *testing.T) {
	h := newHarness(t)
	turnID := settledSuggestionTurn(t, h)
	h.svc.SetPromptSuggestionRunner(func(ctx context.Context, _ domain.SessionID, _, _ string) (string, error) {
		if err := h.st.UpsertActivity(ctx, h.ctrl.ConversationID(), "provider-turn-1", domain.ConversationActivity{
			ID: "late-activity", ProviderItemID: "late-item", Kind: domain.ActivityKindUsage,
			Status: domain.ActivityStatusCompleted, Summary: "Updated usage",
		}, h.now()); err != nil {
			t.Fatal(err)
		}
		return "Add a regression test.", nil
	})
	if prompt, err := h.svc.PromptSuggestion(context.Background(), testSession, turnID); err != nil || prompt != "Add a regression test." {
		t.Fatalf("activity update discarded suggestion: %q, %v", prompt, err)
	}
}

func TestPromptSuggestionReturnsEmptyForNoUsefulOrOversizedAnswer(t *testing.T) {
	h := newHarness(t)
	turnID := settledSuggestionTurn(t, h)
	for _, raw := range []string{"NONE", " none\n", "", strings.Repeat("界", 241)} {
		h.svc.SetPromptSuggestionRunner(func(context.Context, domain.SessionID, string, string) (string, error) { return raw, nil })
		if prompt, err := h.svc.PromptSuggestion(context.Background(), testSession, turnID); err != nil || prompt != "" {
			t.Fatalf("answer %q: prompt=%q err=%v", raw, prompt, err)
		}
	}
}

func TestPromptSuggestionLimitsConcurrentProviderCallsAndHonorsCancellation(t *testing.T) {
	h := newHarness(t)
	turnID := settledSuggestionTurn(t, h)
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	h.svc.SetPromptSuggestionRunner(func(ctx context.Context, _ domain.SessionID, _, _ string) (string, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := h.svc.PromptSuggestion(ctx, testSession, turnID)
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("suggestion runner did not start")
		}
	}
	if prompt, err := h.svc.PromptSuggestion(ctx, testSession, turnID); err != nil || prompt != "" || calls.Load() != 2 {
		t.Fatalf("capacity result = %q, %v; calls=%d", prompt, err, calls.Load())
	}
	cancel()
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	}
}
