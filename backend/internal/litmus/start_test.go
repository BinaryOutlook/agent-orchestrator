package litmus

import (
	"context"
	"errors"
	"testing"
)

func TestStartSpawnsOneClaudeCodeWorkerForThePR(t *testing.T) {
	ctx := context.Background()
	var got []SpawnRequest
	svc := New(NewStore(t.TempDir()), nil).WithSpawner(func(_ context.Context, r SpawnRequest) (string, error) {
		got = append(got, r)
		return "ses-9", nil
	})

	st, err := svc.Start(ctx, StartRequest{ProjectID: " p1 ", PRURL: "acme/web#42"}) // short ref, no harness
	if err != nil || st.SessionID != "ses-9" || st.Name != "litmus: PR #42" {
		t.Fatalf("Start = %+v, %v", st, err)
	}
	want := SpawnRequest{ProjectID: "p1", Harness: "claude-code", Name: "litmus: PR #42",
		Prompt: "Test PR https://github.com/acme/web/pull/42. Start with `ao testing intake https://github.com/acme/web/pull/42`. " + ShellRule + " " + ExploreRule}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("spawn request = %+v, want %+v", got, want)
	}

	is(t, "TESTING_HARNESS_UNSUPPORTED")(svc.Start(ctx, StartRequest{PRURL: "acme/web#42", Harness: "codex"}))
	is(t, "TESTING_PR_URL_INVALID")(svc.Start(ctx, StartRequest{PRURL: "https://gitlab.com/a/b/-/merge_requests/1"}))
	if len(got) != 1 {
		t.Fatalf("a refused start spawned: %+v", got)
	}

	boom := errors.New("spawn failed")
	failing := New(NewStore(t.TempDir()), nil).WithSpawner(func(context.Context, SpawnRequest) (string, error) { return "", boom })
	if _, err := failing.Start(ctx, StartRequest{PRURL: "acme/web#42"}); !errors.Is(err, boom) {
		t.Fatalf("spawn error not passed through: %v", err)
	}
	is(t, "TESTING_SPAWN_UNAVAILABLE")(New(NewStore(t.TempDir()), nil).Start(ctx, StartRequest{PRURL: "acme/web#42"}))
}
