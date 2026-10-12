package litmus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gatedRunner blocks the first runner call until gate closes, so a prepare can be caught in flight.
type gatedRunner struct {
	inner *fakeRunner
	gate  chan struct{}
	once  sync.Once
}

func (g *gatedRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	g.once.Do(func() { <-g.gate })
	return g.inner.Run(ctx, dir, env, name, args...)
}

func TestPrepareRetryWaitsForTheBuildInFlight(t *testing.T) {
	svc, f := prepSvc(t, map[string]string{"v24": "v24.21.0\n"})
	if _, err := svc.Intake(ctx, "s1", prURL); err != nil {
		t.Fatal(err)
	}
	gate := &gatedRunner{inner: f, gate: make(chan struct{})}
	svc.runner = gate
	type res struct {
		run Run
		err error
	}
	first := make(chan res, 1)
	go func() { r, err := svc.Prepare(ctx, "s1"); first <- res{r, err} }()
	deadline := time.Now().Add(5 * time.Second)
	for { // status shows the first prepare in flight once it has begun
		st, err := svc.Status(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if len(st.InFlight) == 1 && st.InFlight[0].Verb == "prepare" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prepare never showed in flight: %+v", st.InFlight)
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := make(chan res, 1)
	go func() { r, err := svc.Prepare(ctx, "s1"); second <- res{r, err} }()
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Prepare(cctx, "s1"); !errors.Is(err, context.Canceled) { // a waiter that gives up gets its own error, not busy
		t.Fatalf("cancelled waiter err = %v", err)
	}
	close(gate.gate)
	for _, c := range []chan res{first, second} {
		select {
		case r := <-c:
			if r.err != nil || r.run.State != RunPrepared {
				t.Fatalf("run state %q err %v", r.run.State, r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("prepare did not finish")
		}
	}
	if n := f.count("clone"); n != 2 { // the second prepare reused the clones the first made
		t.Fatalf("clones = %d, want 2 (head and base once)", n)
	}
	if st, err := svc.Status(ctx, "s1"); err != nil || len(st.InFlight) != 0 {
		t.Fatalf("after prepare: in flight %+v err %v", st.InFlight, err)
	}
}
