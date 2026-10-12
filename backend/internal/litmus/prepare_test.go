package litmus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner stands in for git, npm, cp and node: it records "dir|cmd args" lines and
// mimics just enough of their effects on disk for prepare's decisions to be real.
type fakeRunner struct {
	calls   []string
	node    map[string]string // node binary -> `--version` output
	heads   map[string]string // clone dir -> checked-out SHA
	missing map[string]bool   // SHAs the local checkout lacks until fetched
	noFetch bool
	sh      func(script string) error
	shOut   string            // what a failing `sh` step printed
	gitOut  map[string]string // `git -C dir <sub> <arg>` output, keyed "<sub> <arg>"
	gitErr  map[string]error  // the same key, for a git call that fails
}

func (f *fakeRunner) Run(_ context.Context, dir string, _ []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, dir+"|"+name+" "+strings.Join(args, " "))
	last := args[len(args)-1]
	switch {
	case strings.HasSuffix(name, "node"):
		return []byte(f.node[name]), nil
	case name == "cp":
		return nil, os.MkdirAll(last, 0o750)
	case name == "sh" && f.sh != nil:
		return []byte(f.shOut), f.sh(last)
	case name == "sh" && strings.HasPrefix(last, "npm ci"):
		return nil, os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o750)
	case name != "git":
		return nil, nil
	case args[0] == "clone":
		if err := os.MkdirAll(filepath.Join(last, "frontend"), 0o750); err != nil {
			return nil, err
		}
		os.MkdirAll(filepath.Join(last, ".git"), 0o750)
		return nil, os.WriteFile(filepath.Join(last, "frontend", "package-lock.json"), []byte("lock"), 0o600)
	case len(args) > 3 && f.gitErr[args[2]+" "+args[3]] != nil:
		return nil, f.gitErr[args[2]+" "+args[3]]
	case len(args) > 3 && f.gitOut[args[2]+" "+args[3]] != "":
		return []byte(f.gitOut[args[2]+" "+args[3]]), nil
	case args[2] == "checkout":
		f.heads[args[1]] = last
	case args[2] == "rev-parse" && args[3] == "HEAD":
		return []byte(f.heads[args[1]]), nil
	case args[2] == "rev-parse":
		if sha := strings.TrimSuffix(last, "^{commit}"); !f.missing[sha] {
			return []byte(sha + "\n"), nil
		}
		return nil, os.ErrNotExist
	case args[2] == "fetch" && !f.noFetch:
		delete(f.missing, last)
	}
	return nil, nil
}

func (f *fakeRunner) count(sub string) (n int) {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func prepSvc(t *testing.T, versions map[string]string) (*Service, *fakeRunner) {
	t.Helper()
	bin := t.TempDir()
	f := &fakeRunner{node: map[string]string{}, heads: map[string]string{}, missing: map[string]bool{}}
	for name, version := range versions {
		dir := filepath.Join(bin, name)
		os.MkdirAll(dir, 0o750)
		os.WriteFile(filepath.Join(dir, "node"), nil, 0o700)
		f.node[filepath.Join(dir, "node")] = version
	}
	svc, _ := newSvc(t)
	svc.runner, svc.nodeDirs = f, func(string) []string { return []string{filepath.Join(bin, "*")} }
	svc.WithRepos(func(context.Context, ...string) (string, error) { return "/local", nil })
	return svc, f
}

func TestPrepareLegCacheDecisions(t *testing.T) {
	svc, f := prepSvc(t, map[string]string{"v22": "v22.1.0\n", "v24": "v24.21.0\n"})
	r, _ := ParseRecipe(defaultRecipe)
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	prep := func(sha string) (Leg, Manifest) {
		t.Helper()
		leg, err := svc.prepareLeg(ctx, DefaultRepo, r, "head", sha)
		if err != nil {
			t.Fatal(err)
		}
		return leg, readManifest(leg.Worktree)
	}

	leg, m := prep(shaA) // first clone: nothing to copy from, so a real install
	if m.Install != "npm ci" || f.count("npm ci") != 1 || f.count("clone --shared --no-checkout /local "+leg.Worktree) != 1 || f.count("npm run predev") != 1 {
		t.Fatalf("manifest %+v calls %q", m, f.calls)
	}
	if m.NodeVersion != "v24.21.0" || !strings.HasSuffix(m.NodePath, "v24/node") || m.SHA != shaA || m.InstalledAt.IsZero() || m.PredevAt.IsZero() || m.Durations["install"] < 0 {
		t.Fatalf("manifest %+v", m)
	}
	if leg.BuildManifest != manifestPath(leg.Worktree) || leg.State != "prepared" || filepath.Base(leg.Worktree) != shaA {
		t.Fatalf("leg %+v", leg)
	}

	legB, m := prep(shaB) // same lockfile and node: copy-on-write from the first clone, but predev still runs
	if !strings.HasPrefix(m.Install, "copied from "+shaA) || f.count("npm ci") != 1 || f.count("cp -c -R "+filepath.Join(leg.Worktree, "frontend", "node_modules")+" "+filepath.Join(legB.Worktree, "frontend", "node_modules")) != 1 || f.count("npm run predev") != 2 {
		t.Fatalf("manifest %+v calls %q", m, f.calls)
	}

	before := len(f.calls)
	if _, m = prep(shaA); m.Install != "warm" || f.count("clone") != 2 || f.count("npm run predev") != 2 || len(f.calls)-before > 4 { // only git probes
		t.Fatalf("warm rerun: %+v calls %q", m, f.calls[before:])
	}

	h1, _ := lockHash(leg.Worktree, r.Install.CacheKey, "24")
	h2, _ := lockHash(leg.Worktree, r.Install.CacheKey, "22")
	os.WriteFile(filepath.Join(leg.Worktree, "frontend", "package-lock.json"), []byte("changed"), 0o600)
	if h3, _ := lockHash(leg.Worktree, r.Install.CacheKey, "24"); h1 == h2 || h1 == h3 {
		t.Fatal("lock hash ignores node major or lock contents")
	}
}

func TestPrepareLegFailures(t *testing.T) {
	r, _ := ParseRecipe(defaultRecipe)
	sha := strings.Repeat("c", 40)
	svc, _ := prepSvc(t, map[string]string{"v22": "v22.1.0"}) // no Node 24 anywhere
	is(t, "TESTING_NODE_MISSING")(svc.prepareLeg(ctx, DefaultRepo, r, "head", sha))

	svc, f := prepSvc(t, map[string]string{"v24": "v24.1.0"})
	is(t, "TESTING_SHA_INVALID")(svc.prepareLeg(ctx, DefaultRepo, r, "head", "main"))
	f.missing[sha] = true // absent locally: one fetch from origin makes it appear
	if _, err := svc.prepareLeg(ctx, DefaultRepo, r, "head", sha); err != nil || f.count("fetch origin "+sha) != 1 {
		t.Fatalf("err %v calls %q", err, f.calls)
	}
	for name, resolve := range map[string]RepoResolver{
		"nil":    nil,
		"error":  func(context.Context, ...string) (string, error) { return "", os.ErrPermission },
		"absent": func(context.Context, ...string) (string, error) { return "", nil },
	} {
		svc.resolve = resolve
		_, err := svc.prepareLeg(ctx, DefaultRepo, r, "head", sha, "other/name")
		is(t, "TESTING_REPO_UNRESOLVED")(nil, err)
		if name == "error" && err.Error() != os.ErrPermission.Error() || name == "absent" && !strings.Contains(err.Error(), DefaultRepo+" or other/name") {
			t.Errorf("%s resolver: err = %v", name, err)
		}
	}
	svc.WithRepos(func(context.Context, ...string) (string, error) { return "/local", nil })
	f.noFetch, f.missing[sha] = true, true
	is(t, "TESTING_SHA_MISSING")(svc.prepareLeg(ctx, DefaultRepo, r, "head", sha))
	sha = strings.Repeat("d", 40)
	f.noFetch, f.sh = false, func(string) error { return os.ErrClosed }
	if _, err := svc.prepareLeg(ctx, DefaultRepo, r, "head", sha); err == nil { // a failed predev must be retried next time
		t.Fatal("failed predev reported success")
	} else if m := readManifest(filepath.Join(svc.store.reposDir(DefaultRepo), sha)); m.InstalledAt.IsZero() || !m.PredevAt.IsZero() {
		t.Fatalf("manifest %+v", m)
	}
}

func TestSeededNamesReadAsSamplesAndCarryTheSessionNumber(t *testing.T) {
	r, err := ParseRecipe(defaultRecipe)
	is(t, "")(nil, err)
	env := legEnv(r, "run_ao-9", "head", "/run/head", "/clone", "", 3200, 9400)
	expand := func(cmd string) string { return os.Expand(cmd, func(k string) string { return envValue(env, k) }) }
	project, chat := expand(r.Seed[0].Cmd), expand(r.Seed[1].Cmd)
	// the id stays unique per leg and run (the shared clone's branches are named after it); only the sidebar name changes
	if !strings.Contains(project, `--id "litmus-head-run_ao-9" --name "Sample project 9"`) || !strings.Contains(chat, `--project "litmus-head-run_ao-9" --name "Sample chat 9"`) {
		t.Fatalf("seed commands:\n%s\n%s", project, chat)
	}
	for in, want := range map[string]string{"run_ao-9": "9", "run_ao-19": "19", "run_s1": "1", "run_live": "live", "run_42": "42"} {
		if got := runNumber(in); got != want {
			t.Errorf("runNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecipeParsingAndRefs(t *testing.T) {
	r, err := ParseRecipe(defaultRecipe)
	if err != nil || r.Kind != "electron" || r.Toolchain.Node != "24" || r.Install.CacheKey[0] != "frontend/package-lock.json" || !r.Isolation ||
		!strings.Contains(strings.Join(r.Run.Args, " "), "$LITMUS_DEBUG_PORT") || len(r.Seed) != 2 || !strings.Contains(r.Seed[1].Cmd, "--harness claude-code --mode chat") || strings.Contains(strings.ToLower(string(defaultRecipe)), "fake") || !strings.Contains(r.Seed[1].WaitFor, "session ls") || r.Ready.PageTargets != 1 {
		t.Fatalf("default recipe %+v err %v", r, err)
	}
	for src, want := range map[string]string{
		"": "empty", "kind: cli\nbogus: 1\nrun: {cmd: x}": "bogus", "kind: gui\nrun: {cmd: x}": "kind", "kind: cli": "run.cmd",
		"kind: cli\nrun: {cmd: x, dir: ../up}": "relative", "kind: cli\nrun: {cmd: x}\ntoolchain: {node: '2*'}": "toolchain.node", "kind: cli\nrun: {cmd: x}\ninstall: {cmd: npm ci}": "cacheKey",
	} {
		if _, err := ParseRecipe([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
	}

	st := NewStore(t.TempDir())
	is(t, "")(st.LoadRecipe(DefaultRepo)) // embedded default
	is(t, "TESTING_RECIPE_MISSING")(st.LoadRecipe("o/r"))
	is(t, "")(nil, os.MkdirAll(filepath.Dir(st.recipePath("o/r")), 0o750))
	is(t, "")(nil, os.WriteFile(st.recipePath("o/r"), []byte("kind: cli\nrun: {cmd: ./go}\n"), 0o600))
	if got, err := st.LoadRecipe("o/r"); err != nil || got.Run.Cmd != "./go" {
		t.Fatalf("stored recipe %+v err %v", got, err)
	}
	is(t, "")(nil, os.WriteFile(st.recipePath("o/r"), []byte("kind: cli\n"), 0o600))
	is(t, "TESTING_RECIPE_INVALID")(st.LoadRecipe("o/r"))

	sha := strings.Repeat("e", 40)
	if ref, err := RecipeRef("/a@b/r.yaml", sha); err != nil {
		t.Fatal(err)
	} else if path, got := SplitRecipeRef(ref); path != "/a@b/r.yaml" || got != sha {
		t.Fatalf("split %q %q", path, got)
	}
	if path, got := SplitRecipeRef("/a/r@cafe.yaml"); path != "/a/r@cafe.yaml" || got != "" {
		t.Fatalf("split %q %q", path, got)
	}
	if _, err := RecipeRef("/x", "main"); err == nil {
		t.Fatal("non-hex sha accepted")
	}
	for url, want := range map[string]bool{
		"https://github.com/OrchestratorInc/agent-orchestrator.git": true, "git@github.com:orchestratorinc/agent-orchestrator.git": true,
		"https://gitlab.com/orchestratorinc/agent-orchestrator": false, "https://github.com/orchestratorinc/other": false, "/Users/me/checkout": false, "": false,
	} {
		if MatchRepo(url, "orchestratorinc", "agent-orchestrator") != want {
			t.Errorf("MatchRepo(%q) != %v", url, want)
		}
	}
}

func TestPruneCloneDeletesOnlyBranchesNoWorktreeHolds(t *testing.T) {
	svc, _ := newSvc(t)
	f := &fakeRunner{gitOut: map[string]string{
		"worktree list":                          "worktree /clone\nHEAD abc\n\nworktree /w/live\nHEAD def\nbranch refs/heads/ao/litmus-head-run_s2/root\n",
		"for-each-ref --format=%(refname:short)": "ao/litmus-head-1/root\nao/litmus-head-run_s2/root\nao/litmus-base-1/root\n",
	}}
	svc.runner = f
	clone := t.TempDir()
	is(t, "")(nil, svc.pruneClone(ctx, ""))
	is(t, "")(nil, svc.pruneClone(ctx, clone)) // not a git clone: nothing to prune
	if len(f.calls) != 0 {
		t.Fatalf("git ran on a non-clone: %q", f.calls)
	}
	is(t, "")(nil, os.MkdirAll(filepath.Join(clone, ".git"), 0o750))
	is(t, "")(nil, svc.pruneClone(ctx, clone))
	if f.count("worktree prune") != 1 || f.count("branch -D ao/litmus-head-1/root ao/litmus-base-1/root") != 1 {
		t.Fatalf("calls %q", f.calls)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "branch -D") && strings.Contains(c, "run_s2") {
			t.Fatalf("deleted a branch a worktree holds: %q", c)
		}
	}
}
