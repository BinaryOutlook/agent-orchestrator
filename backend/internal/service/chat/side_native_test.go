package chat_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestSideNativeCurrentUsesCapabilitiesAndIndependentHost(t *testing.T) {
	for _, provider := range []domain.AgentHarness{domain.HarnessClaudeCode, domain.HarnessOpenCode, domain.HarnessOpenCodeV2, domain.HarnessKimi, domain.HarnessOMP, domain.HarnessPi} {
		t.Run(string(provider), func(t *testing.T) {
			h := newSideWorkflow(t, "", provider)
			caps := productionCaps()
			caps[ports.ChatCapabilityForkCurrent] = true
			h.conv.setCapabilities(caps)
			h.source(t)
			side := h.side(t, "native", true)
			if side.ContextMode != "native" || side.NativeAnchorID != "" || side.SeedHistory != "" {
				t.Fatalf("context mode: %+v", side)
			}
			h.driver.mu.Lock()
			defer h.driver.mu.Unlock()
			if len(h.driver.anchors) != 1 || h.driver.anchors[0] != "" || h.driver.sources[0] != h.conv.ProviderConversationID() ||
				h.driver.configs[0].SessionID == testSession || h.driver.configs[0].ProviderScopeID != side.ID {
				t.Fatal("did not create an isolated current-context fork")
			}
		})
	}
}

func TestSideNativeCurrentKeepsParentQuiescentAndReleasesFailedFork(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			h := newSideWorkflow(t, "", domain.HarnessKimi)
			caps := productionCaps()
			caps[ports.ChatCapabilityForkCurrent] = true
			h.conv.setCapabilities(caps)
			h.source(t)
			started, release := make(chan struct{}), make(chan struct{})
			h.driver.onFork = func(ctx context.Context) error {
				close(started)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
				}
				if fail {
					return errors.New("fork failed")
				}
				return nil
			}
			side, err := h.svc.CreateIndependentSideChat(t.Context(), testSession, chatsvc.SideCreateRequest{IdempotencyKey: "fenced", ForceNew: true})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("fork did not start")
			}
			sendCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			_, err = h.svc.Send(sendCtx, testSession, ports.ChatUserMessage{Text: "must not change the cutoff"})
			cancel()
			if err == nil {
				t.Fatal("parent accepted work while its current-context fork was being copied")
			}
			close(release)
			h.awaitSide(t, side.ID, func(s domain.SideSnapshot) bool {
				if fail {
					return s.Side.State == "failed"
				}
				return s.Side.State == "ready"
			})
			if _, err := h.svc.Send(t.Context(), testSession, ports.ChatUserMessage{Text: "parent continues", ClientMessageID: "after-fork"}); err != nil {
				t.Fatalf("parent intake remained fenced: %v", err)
			}
		})
	}
}

func TestSideCurrentForkReconstructsWhenParentHasLaterWork(t *testing.T) {
	h := newSideWorkflow(t, "", domain.HarnessOMP)
	caps := productionCaps()
	caps[ports.ChatCapabilityForkCurrent] = true
	h.conv.setCapabilities(caps)
	h.source(t)
	if _, err := h.svc.Send(t.Context(), testSession, ports.ChatUserMessage{Text: "in flight", ClientMessageID: "running"}); err != nil {
		t.Fatal(err)
	}
	side := h.side(t, "busy", true)
	if side.ContextMode != "reconstructed" || side.SeedHistory == "" || len(h.driver.anchors) != 0 {
		t.Fatalf("unsafe current-context fork: %+v", side)
	}
}

func TestSideCreateWakesHibernatedParentBeforeNativeFork(t *testing.T) {
	h, old := settledHibernationHarness(t, domain.TurnStateCompleted)
	if hibernated, err := h.svc.HibernateChat(t.Context(), testSession); err != nil || !hibernated {
		t.Fatalf("hibernate: %v %v", hibernated, err)
	}
	resumed := newFakeConversation()
	caps := productionCaps()
	caps[ports.ChatCapabilityForkCurrent] = true
	resumed.setCapabilities(caps)
	driver := &connectedSideForkDriver{fakeDriver: fakeDriver{conv: resumed}}
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers: fakeRegistry{driver: driver}, AppRunID: "wake-side",
		Activity: h.activity, Now: h.now,
		NewID: uuid.NewString,
	})
	t.Cleanup(func() { svc.StopAll(context.Background()) })
	svc.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		record, found, err := h.st.GetSession(ctx, id)
		if err != nil {
			return fmt.Errorf("load hibernated parent: %w", err)
		}
		if !found || record.HibernatedAt == nil {
			return errors.New("missing hibernated parent")
		}
		if _, err := h.st.SetSessionHibernated(ctx, id, record.Revision, nil); err != nil {
			return err
		}
		_, err = svc.Start(ctx, chatsvc.StartConfig{
			SessionID: id, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: t.TempDir(), ProviderConversationID: old.ProviderConversationID(),
		})
		return err
	})
	if err := svc.InitializeSideChats(t.Context()); err != nil {
		t.Fatal(err)
	}
	side, err := svc.CreateIndependentSideChat(t.Context(), testSession, chatsvc.SideCreateRequest{IdempotencyKey: "wake"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := svc.SideSnapshot(t.Context(), testSession, side.ID, time.Time{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Side.State == "failed" {
			t.Fatal(snapshot.Side.ErrorMessage)
		}
		if snapshot.Side.State == "ready" {
			if snapshot.Side.ContextMode != "native" || !svc.HasLiveChatController(testSession) {
				t.Fatal("parent was not resumed before forking")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("native side did not open after parent wake")
}
