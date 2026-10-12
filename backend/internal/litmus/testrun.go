package litmus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	testCap     = 3 * time.Minute // longest one `test` verb runs, inside the run's own budget
	maxTests    = 6               // test actions per leg
	testTail    = 40              // lines of output handed back to the tester
	testFileCap = 4 << 20         // bytes of output kept in the receipt file
)

// TestResult is what the tester gets back from the test verb; the full output is in Receipt.
type TestResult struct {
	Leg      string   `json:"leg"`
	Args     []string `json:"args"`
	Exit     int      `json:"exit"`
	TimedOut bool     `json:"timedOut,omitempty"`
	Seconds  float64  `json:"seconds"`
	Summary  string   `json:"summary,omitempty"` // counts, when the output has them
	// WithTestsFrom and CopiedFiles are set when the PR's test files were run against the base checkout.
	WithTestsFrom string   `json:"withTestsFrom,omitempty"`
	CopiedFiles   []string `json:"copiedFiles,omitempty"`
	Output        string   `json:"output"`  // the last 40 lines, stdout and stderr together
	Receipt       string   `json:"receipt"` // file holding the full output
	Seq           int      `json:"seq"`
}

// testAllowed says whether argv is a test command: the first words decide, nothing else runs.
func testAllowed(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	switch argv[0] + " " + argv[1] {
	case "go test", "go vet", "npm test", "npx vitest", "npx jest", "pnpm test":
	case "npm run":
		if len(argv) < 3 || !strings.HasPrefix(argv[2], "test") {
			return false
		}
	default:
		return false
	}
	// go test and go vet can name any program to run the binary, the toolchain or the vet pass with.
	return !slices.ContainsFunc(argv[2:], runsAProgram)
}

// runsAProgram is true for -exec, -toolexec and -vettool, with or without =value.
func runsAProgram(a string) bool {
	a = strings.TrimLeft(strings.ToLower(a), "-")
	for _, name := range []string{"exec", "toolexec", "vettool"} {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// testEnvKeep is what the code under test inherits from the daemon; nothing else (no tokens, no cloud keys, no AO_*
// or CLAUDE*) reaches a PR-controlled test script.
var testEnvKeep = []string{"HOME", "USER", "SHELL", "LANG", "LC_ALL", "TERM", "TMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE", "GOPROXY"}

// testEnv is built from an allow-list: PATH with nodeDir first, the names in testEnvKeep, GOFLAGS when it names no
// program to run, and CI=1.
func testEnv(nodeDir string) []string {
	pathEnv := os.Getenv("PATH")
	if nodeDir != "" {
		pathEnv = nodeDir + string(os.PathListSeparator) + pathEnv
	}
	env := []string{"PATH=" + pathEnv, "CI=1"}
	for _, name := range testEnvKeep {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	if v, ok := os.LookupEnv("GOFLAGS"); ok && !slices.ContainsFunc(strings.Fields(v), runsAProgram) {
		env = append(env, "GOFLAGS="+v)
	}
	return env
}

// lookIn finds name on the PATH of env, since the daemon's own PATH is not the child's.
func lookIn(env []string, name string) (string, error) {
	for _, kv := range env {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, d := range filepath.SplitList(p) {
				if st, err := os.Stat(filepath.Join(d, name)); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
					return filepath.Join(d, name), nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s not found on PATH", name)
}

var (
	goOK       = regexp.MustCompile(`(?m)^ok[ \t]+\S`)
	goFail     = regexp.MustCompile(`(?m)^FAIL[ \t]+\S`)
	goTestFail = regexp.MustCompile(`(?m)^\s*--- FAIL`)
	jsCounts   = regexp.MustCompile(`(?m)^\s*Tests:?\s+.*\b(passed|failed)\b.*$`)
)

// testSummary is one line of counts for go test, or the "Tests: N passed, M failed" line of vitest and jest.
func testSummary(argv []string, out string) string {
	if argv[0] == "go" {
		if argv[1] != "test" {
			return ""
		}
		return fmt.Sprintf("%d ok, %d FAIL, %d --- FAIL", len(goOK.FindAllString(out, -1)), len(goFail.FindAllString(out, -1)), len(goTestFail.FindAllString(out, -1)))
	}
	return strings.TrimSpace(jsCounts.FindString(out))
}

// Test runs an allow-listed test command inside a prepared leg's checkout, capped at three minutes and at the
// run's deadline, and records it like any other action: a line in actions.jsonl, the full output in
// receipts/<n>-test.txt, and a receipt on the open check. It does not need the leg launched.
//
// With withTestsFrom "head" (base leg only) the PR's added or changed test files are first copied from the head
// checkout into a throwaway git worktree of the base checkout, and the command runs there; the base checkout is
// never touched.
func (s *Service) Test(ctx context.Context, sessionID, leg, dir string, argv []string, withTestsFrom string) (TestResult, error) {
	if leg != "base" && leg != "head" {
		return TestResult{}, invalid("TESTING_LEG_INVALID", "name the leg with --leg head|base")
	}
	if withTestsFrom != "" && (withTestsFrom != "head" || leg != "base") {
		return TestResult{}, invalid("TESTING_TEST_NOT_ALLOWED", "--with-tests-from head works only with --leg base")
	}
	if len(argv) == 0 {
		return TestResult{}, invalid("TESTING_ARGS_INVALID", "wrong number of arguments; usage: test --leg head|base --dir <subdir> -- <command...>")
	}
	if !testAllowed(argv) {
		return TestResult{}, invalid("TESTING_TEST_NOT_ALLOWED", "only go test, go vet, npm test, npm run test*, npx vitest, npx jest and pnpm test run here; no -exec or -toolexec")
	}
	if dir != "" && !filepath.IsLocal(dir) {
		return TestResult{}, invalid("TESTING_TEST_DIR_INVALID", "--dir must be a folder inside the checkout, like backend")
	}
	run, done, err := s.begin(sessionID, "test "+leg)
	if err != nil {
		return TestResult{}, err
	}
	defer done()
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg && l.Worktree != "" })
	if i < 0 || (run.State != RunPrepared && run.State != RunRunning) {
		return TestResult{}, conflict("TESTING_LEG_NOT_PREPARED", fmt.Sprintf("the %s leg is not prepared; run `ao testing prepare`", leg))
	}
	cwd := filepath.Join(run.Legs[i].Worktree, dir)
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return TestResult{}, invalid("TESTING_TEST_DIR_INVALID", fmt.Sprintf("no folder %q in the %s checkout", dir, leg))
	}
	nodeDir := ""
	if p := readManifest(run.Legs[i].Worktree).NodePath; p != "" {
		nodeDir = filepath.Dir(p)
	}
	runDir := filepath.Join(s.store.root, run.ID)
	if n := len(slices.DeleteFunc(readActions(runDir), func(a actionRec) bool { return a.Action != "test" || cmp.Or(a.Leg, "head") != leg })); n >= maxTests {
		return TestResult{}, conflict("TESTING_TEST_LIMIT", fmt.Sprintf("at most %d test runs per leg; use what you have", maxTests))
	}

	var files []string
	headDir := ""
	if withTestsFrom != "" {
		patch, _ := os.ReadFile(filepath.Join(runDir, "diff.patch"))
		if files = prTestFiles(string(patch)); len(files) == 0 {
			return TestResult{}, invalid("TESTING_TEST_NOT_ALLOWED", "the PR adds no test files")
		}
		h := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == "head" && l.Worktree != "" })
		if h < 0 {
			return TestResult{}, conflict("TESTING_LEG_NOT_PREPARED", "the head leg is not prepared; run `ao testing prepare`")
		}
		headDir = run.Legs[h].Worktree
	}

	s.mu.Lock()
	seq := s.nextSeq(run.ID)
	s.mu.Unlock()
	file := filepath.Join(runDir, "receipts", fmt.Sprintf("%d-test.txt", seq))
	limit := s.testCap
	if left := run.Deadline.Sub(s.clock()); left < limit {
		limit = left
	}
	// The request's own 60 s deadline is shorter than the cap; like prepare, this verb outlives it.
	var res TestResult
	if withTestsFrom == "" {
		res, err = execTest(context.WithoutCancel(ctx), cwd, argv, testEnv(nodeDir), limit, file)
	} else {
		res, err = s.testWithHeadTests(context.WithoutCancel(ctx), runDir, run.Legs[i].Worktree, headDir, dir, files, argv, testEnv(nodeDir), limit, file)
	}
	if err != nil {
		return TestResult{}, apierr.Unavailable("TESTING_TEST_FAILED", err.Error())
	}
	res.Leg, res.Args, res.Receipt, res.Seq = leg, argv, file, seq

	rec := actionRec{Seq: seq, Leg: leg, Action: "test", Args: argv, At: s.clock(), MS: int64(res.Seconds * 1000), Exit: &res.Exit, WithTestsFrom: res.WithTestsFrom, CopiedFiles: res.CopiedFiles}
	s.attach(run.ID, &rec, Receipt{Seq: seq, Leg: leg, Action: "test", Args: argv, OutputPath: file, At: rec.At})
	return res, nil
}

// execTest runs argv in cwd with env and writes the command line, the output and the outcome to file (output past 4 MB is dropped).
// At limit it kills the whole process group and reports a timeout.
func execTest(ctx context.Context, cwd string, argv, env []string, limit time.Duration, file string) (TestResult, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return TestResult{}, err
	}
	bin, err := lookIn(env, argv[0])
	if err != nil {
		return TestResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, max(limit, 0))
	defer cancel()
	cmd := process.CommandContext(cctx, bin, argv[1:]...) // #nosec G204 -- argv[0] and its first words passed the allow-list.
	cmd.Dir, cmd.Env = cwd, env
	out := &capWriter{max: testFileCap}
	cmd.Stdout, cmd.Stderr = out, out
	ownGroup(cmd)
	var cancelled atomic.Bool
	cmd.Cancel = func() error { cancelled.Store(true); sigkillGroup(cmd.Process.Pid); return nil }
	cmd.WaitDelay = 2 * time.Second // a grandchild that outlives the kill must not hold the pipes open
	t0 := time.Now()
	runErr := cmd.Run()
	if cmd.Process != nil {
		sigkillGroup(cmd.Process.Pid) // a background child must not outlive the verb
	}
	if errors.Is(runErr, exec.ErrWaitDelay) { // exited cleanly, but a leftover child held the pipes open
		runErr = nil
	}
	res := TestResult{Seconds: time.Since(t0).Seconds(), TimedOut: cancelled.Load() && errors.Is(cctx.Err(), context.DeadlineExceeded)}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.Exit = exitErr.ExitCode()
	case res.TimedOut:
		res.Exit = -1
	default:
		return TestResult{}, runErr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "$ %s   (in %s)\n", strings.Join(argv, " "), cwd)
	b.Write(out.buf)
	if res.TimedOut {
		res.Exit = -1
		fmt.Fprintf(&b, "\ntimed out after %s; the process group was killed\n", limit.Round(time.Second))
	}
	fmt.Fprintf(&b, "\nexit %d in %.1fs\n", res.Exit, res.Seconds)
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		return TestResult{}, err
	}
	res.Output = lastLines([]byte(b.String()), testTail)
	res.Summary = testSummary(argv, string(out.buf))
	return res, nil
}

// testFileSuffixes are the file names that count as tests when a PR adds or changes them.
var testFileSuffixes = []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"}

// isTestFile is true for the test files a PR may bring along: *_test.go, *.test.ts(x), *.spec.ts(x) and anything under __tests__.
func isTestFile(p string) bool {
	if slices.ContainsFunc(testFileSuffixes, func(suf string) bool { return strings.HasSuffix(path.Base(p), suf) }) {
		return true
	}
	return slices.Contains(strings.Split(path.Dir(p), "/"), "__tests__")
}

// prTestFiles lists, in diff order, the test files a unified diff adds, changes or renames to (deleted files are
// not listed). Paths are relative to the repo root.
func prTestFiles(diff string) []string {
	var (
		files   []string
		cur     string
		deleted bool
	)
	flush := func() {
		if cur != "" && !deleted && filepath.IsLocal(cur) && isTestFile(cur) && !slices.Contains(files, cur) {
			files = append(files, cur)
		}
		cur, deleted = "", false
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			if _, b, ok := strings.Cut(line, " b/"); ok { // fallback for a binary or mode-only change, which has no +++ line
				cur = gitPath(b)
			}
		case strings.HasPrefix(line, "deleted file mode"):
			deleted = true
		case strings.HasPrefix(line, "rename to "):
			cur = gitPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "+++ b/"):
			cur = gitPath(strings.TrimPrefix(line, "+++ b/"))
		}
	}
	flush()
	return files
}

// gitPath drops the tab git appends after names with spaces.
func gitPath(p string) string { return strings.TrimRight(p, "\t\r") }

// testWithHeadTests runs argv in a throwaway worktree of the base checkout that holds the PR's test files copied
// from the head checkout. The worktree is removed whatever happens; the base checkout's own files are never written.
func (s *Service) testWithHeadTests(ctx context.Context, runDir, baseDir, headDir, dir string, files, argv, env []string, limit time.Duration, receipt string) (TestResult, error) {
	wt := filepath.Join(runDir, "base-tests")
	_, _ = s.git(ctx, baseDir, "worktree", "remove", "--force", wt) // a leftover from a daemon that died mid-run
	_ = os.RemoveAll(wt)
	if _, err := s.git(ctx, baseDir, "worktree", "add", "--detach", wt, "HEAD"); err != nil {
		return TestResult{}, err
	}
	var links []string
	defer func() {
		for _, l := range links { // unlink first, so removing the worktree can never reach the base checkout's node_modules
			_ = os.Remove(l)
		}
		_, _ = s.git(ctx, baseDir, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
		_, _ = s.git(ctx, baseDir, "worktree", "prune")
	}()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(headDir, f))
		if err != nil {
			return TestResult{}, fmt.Errorf("copy %s from the head checkout: %w", f, err)
		}
		dst := filepath.Join(wt, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return TestResult{}, err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return TestResult{}, err
		}
	}
	if slices.Contains([]string{"npm", "npx", "pnpm"}, argv[0]) {
		for _, d := range []string{"frontend", dir, "."} {
			src, dst := filepath.Join(baseDir, d, "node_modules"), filepath.Join(wt, d, "node_modules")
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			if err := os.Symlink(src, dst); err != nil {
				return TestResult{}, err
			}
			links = append(links, dst)
		}
	}
	cwd := filepath.Join(wt, dir)
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return TestResult{}, fmt.Errorf("no folder %q in the base checkout at its commit", dir)
	}
	res, err := execTest(ctx, cwd, argv, env, limit, receipt)
	if err != nil {
		return TestResult{}, err
	}
	res.WithTestsFrom, res.CopiedFiles = "head", files
	full, err := os.ReadFile(receipt)
	if err != nil {
		return TestResult{}, err
	}
	pre := "test files copied from the PR's head checkout onto main's code:\n  " + strings.Join(files, "\n  ") + "\n\n"
	if err := os.WriteFile(receipt, append([]byte(pre), full...), 0o600); err != nil {
		return TestResult{}, err
	}
	if res.Exit != 0 && argv[0] == "go" {
		if n := uncompiledTestFiles(string(full), files); n > 0 {
			res.Summary = fmt.Sprintf("%d test files from the PR do not compile against main", n)
			if n == 1 {
				res.Summary = "1 test file from the PR does not compile against main"
			}
		}
	}
	return res, nil
}

var goCompileErr = regexp.MustCompile(`(?m)^(?:\./)?(\S+\.go):\d+(?::\d+)?: `)

// uncompiledTestFiles counts the copied files that go's compiler output names in an error line; paths there are
// relative to the folder go ran in, so they match the repo-relative name by suffix.
func uncompiledTestFiles(out string, copied []string) int {
	if !strings.Contains(out, "[build failed]") && !strings.Contains(out, "[setup failed]") {
		return 0
	}
	named := map[string]bool{}
	for _, m := range goCompileErr.FindAllStringSubmatch(out, -1) {
		for _, f := range copied {
			if f == m[1] || strings.HasSuffix(f, "/"+m[1]) {
				named[f] = true
			}
		}
	}
	return len(named)
}
