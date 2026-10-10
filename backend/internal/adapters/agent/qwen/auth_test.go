package qwen

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestQwenLocalAuthStatusConfiguredWithProviderEnv(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "zai-key")

	status, ok, err := qwenLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenLocalAuthStatusConfiguredWithDocumentedProtocolEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	status, ok, err := qwenLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusFromSettingsConfiguredWithModelProviderAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{
		"modelProviders": {
			"zai": {
				"baseUrl": "https://api.z.ai/api/coding/paas/v4",
				"apiKey": "zai-key"
			}
		},
		"defaultModel": "glm-4.5"
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusFromSettingsConfiguredWithSecurityAuthAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{
		"security": {
			"auth": {
				"apiKey": "openai-compatible-key"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusFromSettingsConfiguredWithConfiguredEnvKey(t *testing.T) {
	t.Setenv("CUSTOM_QWEN_KEY", "configured-key")
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"modelProviders":{"custom":{"envKey":"CUSTOM_QWEN_KEY"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusFromSettingsConfiguredWithDocumentedEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"GEMINI_API_KEY":"gemini-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenEnvFileAuthStatusConfiguredWithDocumentedProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("OPENROUTER_API_KEY=router-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenEnvFileAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenGlobalEnvAuthStatusUsesQwenHome(t *testing.T) {
	home := t.TempDir()
	qwenHome := filepath.Join(home, "custom-qwen")
	if err := os.MkdirAll(qwenHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qwenHome, ".env"), []byte("GOOGLE_API_KEY=google-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenGlobalEnvAuthStatus(qwenHome, home)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenGlobalEnvAuthStatusIgnoresDaemonWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	qwenHome := filepath.Join(root, "qwen")
	if err := os.MkdirAll(qwenHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("QWEN_API_KEY=project-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	status, ok, err := qwenGlobalEnvAuthStatus(qwenHome, filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestQwenEnvFileAuthStatusConfiguredWithConfiguredProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("CUSTOM_QWEN_KEY=custom-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := qwenEnvFileAuthStatus(path, "CUSTOM_QWEN_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusFromSettingsUnknownWhenMissing(t *testing.T) {
	status, ok, err := qwenAuthStatusFromSettings(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestQwenAuthStatusFromSettingsConfiguredWithOAuthSelectedType(t *testing.T) {
	// An OAuth user records the selected auth type instead of a key. Before
	// this was read, that install reported unknown, which the readiness
	// coordinator counts as a failure and retries on backoff forever.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"security":{"auth":{"selectedType":"qwen-oauth"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := qwenAuthStatusFromSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestQwenAuthStatusNeverReportsAuthorizedFromLocalPresence(t *testing.T) {
	// ports/agent.go requires that presence-only evidence reports configured
	// and never renders as a ready state. Guard every local evidence source.
	cases := []struct {
		name string
		body string
	}{
		{"security auth apiKey", `{"security":{"auth":{"apiKey":"openai-compatible-key"}}}`},
		{"modelProviders apiKey", `{"modelProviders":{"openai":[{"id":"m","apiKey":"k"}]}}`},
		{"documented env map", `{"env":{"DASHSCOPE_API_KEY":"sk-test"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			status, ok, err := qwenAuthStatusFromSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("want configured evidence to be reported")
			}
			if status == ports.AgentAuthStatusAuthorized {
				t.Fatalf("presence-only evidence reported %q; must never report %q", status, ports.AgentAuthStatusAuthorized)
			}
		})
	}
}

func TestQwenAuthStatusWithoutLocalEvidenceStaysUnknown(t *testing.T) {
	for _, name := range qwenAPIKeyEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("QWEN_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	status, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}
