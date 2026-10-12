package litmus

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeGH answers gh calls from canned output keyed by the first two args.
type fakeGH struct {
	t     *testing.T
	out   map[string][]byte
	err   map[string]error
	calls []string
}

func (f *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	key := args[0] + " " + args[1]
	if err := f.err[key]; err != nil {
		return nil, err
	}
	out, ok := f.out[key]
	if !ok && args[0] == "api" { // a submit tries to post its comment; these tests have no GitHub to post to
		return nil, errors.New("gh: no network in tests")
	}
	if !ok {
		f.t.Fatalf("unexpected gh call %v", args)
	}
	return out, nil
}

// fixtureGH serves the recorded output of the real gh commands for PR 6394.
func fixtureGH(t *testing.T) *fakeGH {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return &fakeGH{t: t, err: map[string]error{}, out: map[string][]byte{"pr view": read("pr6394.json"), "pr diff": read("pr6394.diff")}}
}

func readRunFile(t *testing.T, svc *Service, run Run, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePR(t *testing.T) {
	cases := map[string]string{ // input -> "repo#number", or "" when invalid
		"https://github.com/OrchestratorInc/agent-orchestrator/pull/6394": "orchestratorinc/agent-orchestrator#6394",
		"https://www.github.com/o/r/pull/7/":                              "o/r#7",
		" Untrivial-ai/Agent-Orchestrator#12 ":                            "untrivial-ai/agent-orchestrator#12",
	}
	for _, in := range []string{"", "o/r", "o/r#", "o/r#0", "o/r#x", "r#5", "#5", "https://github.com/o/r/issues/1", "https://github.com/o/r/pull/0", "https://github.com/o/r/pull/3/files",
		"https://github.com/o/pull/3", "https://github.com/o/r/pull/3?x=1", "https://gitlab.com/g/p/-/merge_requests/3", "https://acme.ghe.io/o/r/pull/3", "ftp://github.com/o/r/pull/3"} {
		cases[in] = ""
	}
	for in, want := range cases {
		got := ""
		if repo, n, err := ParsePR(in); err == nil {
			got = fmt.Sprintf("%s#%d", repo, n)
		}
		if got != want {
			t.Errorf("ParsePR(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIntakeWritesContextPack(t *testing.T) {
	svc, now := newSvc(t)
	gh := svc.gh.(*fakeGH)
	run, err := svc.Intake(ctx, "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	if run.BaseSHA != "0ca80cfc370b21443c4a2b9d22199c047b8c74e7" || run.HeadSHA != "e2204e0f361867a28310e71421a4227ab1d09544" ||
		run.Repo != "orchestratorinc/agent-orchestrator" || run.State != RunIntake || !run.Deadline.Equal(now.Add(25*time.Minute)) {
		t.Fatalf("run %+v", run)
	}
	if want := []Area{{Name: "Renderer chat", Files: 1, Stems: []string{"TurnSettingsBar"}}, {Name: "Tests", Files: 1}}; run.BaseRef != "main" || !reflect.DeepEqual(run.Areas, want) {
		t.Fatalf("base %q areas %+v, want %+v", run.BaseRef, run.Areas, want)
	}
	// the diff call uses the repo name gh reported, not the renamed one from the URL
	if want := []string{"pr view 6394 --repo aoagents/agent-orchestrator --json " + prFields, "pr diff 6394 --repo orchestratorinc/agent-orchestrator"}; !slices.Equal(gh.calls, want) {
		t.Fatalf("gh calls %q", gh.calls)
	}
	if saved, err := svc.store.Get(run.ID); err != nil || saved.HeadSHA != run.HeadSHA || saved.Repo != run.Repo || saved.Error != "" {
		t.Fatalf("saved %+v err %v", saved, err)
	}
	if want, _ := os.ReadFile("testdata/pr6394.diff"); readRunFile(t, svc, run, "diff.patch") != string(want) {
		t.Fatal("diff.patch is not the whole diff")
	}
	pack, last := readRunFile(t, svc, run, "context-pack.md"), -1
	for _, h := range []string{"## 1. PR", "## 2. What the PR says", "## 3. Linked issue", "## 4. Changed files", "### Diff excerpt", "## 5. CI status", "## 6. Recipe", "## 7. Prior runs on this repo", "## 8. What to do next"} {
		if i := strings.Index(pack, h); i <= last {
			t.Fatalf("heading %q out of order (at %d, previous %d)", h, i, last)
		} else {
			last = i
		}
	}
	for _, want := range []string{"keep model menu open on Other models", "Author: ronishrohan | State: MERGED", "Base: main@0ca80cfc370b", "Head: fix/model-menu-closes-on-selection@e2204e0f3618",
		"Size: 2 files, +43/-16", "### frontend/src/renderer", "components/chat/TurnSettingsBar.tsx +7/-0", "full diff: diff.patch", "- test -> SUCCESS", "None linked.", "none yet", "ao testing launch head", "## Where the code is", "/repos/orchestratorinc__agent-orchestrator/e2204e0f361867a28310e71421a4227ab1d09544", "/repos/orchestratorinc__agent-orchestrator/0ca80cfc370b21443c4a2b9d22199c047b8c74e7"} {
		if !strings.Contains(pack, want) {
			t.Errorf("context-pack.md lacks %q", want)
		}
	}
	if n := len(strings.Fields(pack)); n > maxPackWords {
		t.Fatalf("pack has %d words", n)
	}
}

func TestIntakeIssuePriorRunsRecipeAndCaps(t *testing.T) {
	svc, _ := newSvc(t)
	gh := svc.gh.(*fakeGH)
	user := map[string]any{"login": "u"}
	var pr map[string]any
	if err := json.Unmarshal(gh.out["pr view"], &pr); err != nil {
		t.Fatal(err)
	}
	var comments, issueComments []map[string]any
	for i := 1; i <= 12; i++ {
		comments = append(comments, map[string]any{"author": user, "body": fmt.Sprintf("comment-%02d", i), "createdAt": time.Date(2026, 10, i, 0, 0, 0, 0, time.UTC)})
	}
	for i := 1; i <= 7; i++ {
		issueComments = append(issueComments, map[string]any{"author": user, "body": fmt.Sprintf("issue-comment-%d", i)})
	}
	pr["comments"], pr["body"] = comments, strings.Repeat("word ", 2000)
	pr["reviews"] = []map[string]any{{"author": user, "body": "", "state": "APPROVED", "submittedAt": time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}}
	pr["closingIssuesReferences"] = []map[string]any{{"number": 7, "repository": map[string]any{"name": "agent-orchestrator", "owner": map[string]any{"login": "OrchestratorInc"}}}}
	gh.out["pr view"], _ = json.Marshal(pr)
	gh.out["issue view"], _ = json.Marshal(map[string]any{"title": "Menu closes by itself", "body": "steps to reproduce", "comments": issueComments})

	repo := "orchestratorinc/agent-orchestrator"
	is(t, "")(nil, svc.store.Save(Run{ID: "run_old", Repo: repo, PRNumber: 5}))
	is(t, "")(nil, svc.store.Save(Run{ID: "run_noverdict", Repo: repo, PRNumber: 6}))
	is(t, "")(nil, svc.store.WriteFile("run_old", "verdict.md", []byte("# Verdict: fail\n\nmore")))
	recipes := filepath.Join(svc.store.root, "..", "recipes")
	is(t, "")(nil, os.MkdirAll(recipes, 0o750))
	is(t, "")(nil, os.WriteFile(filepath.Join(recipes, "orchestratorinc__agent-orchestrator.yaml"), []byte("kind: electron\n"), 0o600))

	run, err := svc.Intake(ctx, "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "issue view 7 --repo " + repo + " --json title,body,comments"; gh.calls[2] != want {
		t.Fatalf("issue call %q, want %q", gh.calls[2], want)
	}
	pack := readRunFile(t, svc, run, "context-pack.md")
	for _, want := range []string{"[truncated at 1500 words]", "Menu closes by itself", "issue-comment-5", "comment-04", "run_old (#5): # Verdict: fail", "kind: electron"} {
		if !strings.Contains(pack, want) {
			t.Errorf("context-pack.md lacks %q", want)
		}
	}
	for _, unwanted := range []string{"issue-comment-6", "comment-03", "run_noverdict", "none yet"} {
		if strings.Contains(pack, unwanted) {
			t.Errorf("context-pack.md has %q", unwanted)
		}
	}
	if strings.Index(pack, "comment-12") > strings.Index(pack, "APPROVED") { // newest last
		t.Error("comments are not oldest first")
	}
}

func TestDiffExcerpt(t *testing.T) {
	file := func(name string, added, context int) string {
		s := fmt.Sprintf("diff --git a/%[1]s b/%[1]s\n--- a/%[1]s\n+++ b/%[1]s\n@@ -1 +1 @@\n", name)
		return s + strings.Repeat(" context\n", context) + strings.Repeat("+added line\n", added)
	}
	diff := file("small.go", 2, 0) + file("big.go", 250, 10) + file("mid.go", 60, 0)
	lines := pickDiff(splitDiff(diff), maxDiffLines)
	if len(lines) != maxDiffLines || lines[0] != "diff --git a/big.go b/big.go" || !strings.Contains(lines[len(lines)-1], "file truncated") || strings.Contains(strings.Join(lines, "\n"), "small.go") {
		t.Fatalf("excerpt: %d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	huge := strings.Repeat("+"+strings.Repeat("word ", 20)+"\n", 400)
	pack := renderPack(intakeData{repo: "o/r", pr: pullRequest{Number: 1}, diff: "diff --git a/h b/h\n" + huge}, "", nil)
	if n := len(strings.Fields(pack)); n > maxPackWords || !strings.Contains(pack, "full diff: diff.patch") || !strings.Contains(pack, "```diff") {
		t.Fatalf("pack has %d words or lost its excerpt", n)
	}
}

func TestIntakeGHFailureLeavesNoPartialFiles(t *testing.T) {
	for _, c := range []struct{ key, msg, code string }{
		{"pr view", "GraphQL: Could not resolve to a PullRequest with the number of 6394.", "TESTING_GH_NOT_FOUND"},
		{"pr view", "To get started with GitHub CLI, please run:  gh auth login", "TESTING_GH_AUTH"},
		{"pr view", "HTTP 403: API rate limit exceeded for user ID 1.", "TESTING_GH_RATE_LIMIT"},
		{"pr diff", `exec: "gh": executable file not found in $PATH`, "TESTING_GH_MISSING"}, // view succeeded, diff fails
		{"pr diff", "boom", "TESTING_GH_FAILED"},
	} {
		svc, now := newSvc(t)
		gh := svc.gh.(*fakeGH)
		gh.err[c.key] = errors.New(c.msg)
		run, err := svc.Intake(ctx, "s1", prURL)
		is(t, c.code)(nil, err)
		entries, _ := os.ReadDir(filepath.Join(svc.store.root, run.ID))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Equal(names, []string{"receipts", "run.json"}) { // no diff.patch, context-pack.md or .tmp-* files
			t.Fatalf("%s: files after failure: %v", c.code, names)
		}
		if saved, err := svc.store.Get(run.ID); err != nil || saved.State != RunIntake || saved.Error == "" || saved.HeadSHA != "" {
			t.Fatalf("%s: saved %+v err %v", c.code, saved, err)
		}

		delete(gh.err, c.key) // retry in place restarts the clock
		*now = now.Add(5 * time.Minute)
		retry, err := svc.Intake(ctx, "s1", prURL)
		if err != nil || retry.Error != "" || retry.HeadSHA == "" || !retry.Deadline.Equal(now.Add(RunBudget)) {
			t.Fatalf("%s: retry %+v err %v", c.code, retry, err)
		}
		readRunFile(t, svc, retry, "context-pack.md")
		is(t, "TESTING_RUN_EXISTS")(svc.Intake(ctx, "s1", prURL)) // pinned runs keep the 409
	}
}

func TestIntakeLive(t *testing.T) {
	if os.Getenv("LITMUS_LIVE") != "1" {
		t.Skip("set LITMUS_LIVE=1 to call the real gh CLI")
	}
	svc := New(NewStore(t.TempDir()), nil)
	run, err := svc.Intake(ctx, "live", "https://github.com/OrchestratorInc/agent-orchestrator/pull/6394")
	if err != nil {
		t.Fatal(err)
	}
	pack := readRunFile(t, svc, run, "context-pack.md")
	head := strings.Join(strings.SplitN(pack, "\n", 21)[:20], "\n")
	t.Logf("run %+v\ncontext-pack.md: %d words\n%s", run, len(strings.Fields(pack)), head)
}

func TestAreasOfNamesChangedPartsInPlainWords(t *testing.T) {
	got := areasOf([]string{
		"frontend/src/renderer/shell/sidebar/Sidebar.tsx", "frontend/src/renderer/shell/sidebar/useHistory.ts", "frontend/src/renderer/shell/sidebar/history.ts",
		"backend/internal/litmus/comment_test.go", "frontend/e2e/tests/nav.spec.ts", "frontend/e2e/helpers.ts", "README.md", "package.json", "backend/internal/chat_view/view.go",
		"frontend/src/renderer/components/TitlebarNav.tsx", "frontend/src/renderer/components/chat/Composer.tsx", "backend/internal/litmus/comment.go",
	})
	want := []Area{{Name: "Shell sidebar", Files: 3, Stems: []string{"Sidebar", "useHistory", "history"}}, {Name: "Tests", Files: 3},
		{Name: "Backend chat view", Files: 1, Stems: []string{"view"}}, {Name: "Backend litmus", Files: 1, Stems: []string{"comment"}}, {Name: "Docs", Files: 1, Stems: []string{"README"}},
		{Name: "Project files", Files: 1, Stems: []string{"package"}}, {Name: "Renderer chat", Files: 1, Stems: []string{"Composer"}}, {Name: "Renderer components", Files: 1, Stems: []string{"TitlebarNav"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("areas %+v, want %+v", got, want)
	}
}

// suggestFixture loads testdata/suggest-<n>.json: the title, body and changed files of a real PR.
func suggestFixture(t *testing.T, n string) intakeData {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "suggest-"+n+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var d intakeData
	if err := json.Unmarshal(raw, &d.pr); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSuggestedChecksOnThreeRealPRs(t *testing.T) {
	for n, want := range map[string]string{
		"6403": "## Suggested checks\n\n- Headline claim: keep navigation arrows visible with the sidebar expanded\n- Something visible in the renderer component TitlebarNav still works.\n",
		"6266": "## Suggested checks\n\n- Headline claim: refine Harness settings layout\n- Something visible in the renderer settings (HarnessLogo, HarnessSettingsSection) still works.\n- Something visible in the renderer styles still works.\n",
		"6387": "## Suggested checks\n\n- Headline claim: acknowledge reports inside Claude paste wrappers\n- Something visible in the cli package (hooks) still works.\n",
	} {
		d := suggestFixture(t, n)
		got := suggestedChecks(d)
		t.Logf("#%s:\n%s", n, got)
		if got != want {
			t.Errorf("#%s:\n got %q\nwant %q", n, got, want)
		}
		if pack := renderPack(d, "", nil); !strings.Contains(pack, "\n"+want+"\n") || strings.Index(pack, want) > strings.Index(pack, "## 1. PR") {
			t.Errorf("#%s: the section is not at the top of the pack", n)
		}
		noSlop(t, strings.ReplaceAll(got, "Headline claim", "Main claim"))
	}
}

func TestSuggestedChecksTakesTheClaimSentenceAndTheIssueAndStopsAtFiveLines(t *testing.T) {
	d := intakeData{issue: &issue{Number: 12, Title: "Arrows vanish"}}
	d.pr.Title = "fix(shell)!: keep arrows"
	d.pr.Body = "## What\n\nSome context here.\n\n```\nAdds a code line that is not prose.\n```\n\nThis PR keeps the [arrows](http://x) visible. More text.\n"
	for _, p := range []string{"frontend/src/renderer/components/a/A.tsx", "frontend/src/renderer/hooks/b.ts", "backend/internal/cli/c.go", "backend/internal/store/d.go", "docs/e.md", "backend/internal/cli/c_test.go"} {
		d.pr.Files = append(d.pr.Files, struct {
			Path                 string
			Additions, Deletions int
		}{Path: p})
	}
	got := suggestedChecks(d)
	lines := strings.Split(strings.TrimSpace(strings.SplitN(got, "\n\n", 2)[1]), "\n")
	if len(lines) != 5 || lines[0] != "- Headline claim: keep arrows" || lines[1] != "- In the author's words: This PR keeps the arrows visible." || lines[2] != "- Linked issue #12: Arrows vanish" {
		t.Errorf("lines %q", lines)
	}
	for _, l := range lines[3:] {
		if !strings.HasPrefix(l, "- Something visible in ") || strings.Contains(l, "docs") || strings.Contains(l, "test") {
			t.Errorf("area line %q", l)
		}
	}
	for body, want := range map[string]string{
		"Adds a retry on failure. Other things.":                              "Adds a retry on failure.",
		"Nothing here. Users can now pick a theme.":                           "Users can now pick a theme.",
		"The menu closes so that the list stays visible":                      "The menu closes so that the list stays visible",
		"This PR contains only four frontend files. Adds a retry on failure.": "Adds a retry on failure.",
		"Fixes it.":                            "", // too short to quote
		"The composer now waits. Adding more.": "",
	} {
		if got := claimSentence(body); got != want {
			t.Errorf("claimSentence(%q) = %q, want %q", body, got, want)
		}
	}
}

// mapTree is a codeTree over files held in memory.
type mapTree map[string]string

func (m mapTree) file(path string) []string {
	if s, ok := m[path]; ok {
		return strings.Split(s, "\n")
	}
	return nil
}

func (m mapTree) grep(word string) (hits []codeHit) {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	for _, p := range slices.Sorted(maps.Keys(m)) {
		for i, l := range strings.Split(m[p], "\n") {
			if re.MatchString(l) {
				hits = append(hits, codeHit{p, i + 1})
			}
		}
	}
	return hits
}

// gateFixture is a trimmed copy of the three #6266 files that decide whether the Local/Cloud tabs show.
var gateFixture = mapTree{
	"frontend/src/renderer/components/settings/HarnessSettingsSection.tsx": `import { useCloudGate } from "../../hooks/useCloudGate";
function isActive(job: InstallJob | undefined): boolean {
	return job?.status === "running";
}
export function HarnessSettingsSection() {
	const { cloudEnabled } = useCloudGate();
	const isInstalled = installed.has(agentId);
	return cloudEnabled ? <Tabs className="harness-tabs" /> : null;
}`,
	"frontend/src/renderer/hooks/useCloudGate.ts": `import { useSettings } from "./useSettings";

export interface CloudGate {
	/** Whether the cloud offering is available (flag + entitled client + control plane). */
	cloudEnabled: boolean;
	localEnabled: boolean;
}

export function useCloudGate(enabled = true): CloudGate {
	return { cloudEnabled: settings?.cloudEnabled ?? false, localEnabled: true };
}`,
	"frontend/src/renderer/components/settings/GeneralSettingsSection.tsx": `
/**
 * Developer Mode-only toggle for the cloud offering. Persisted daemon-side (a
 * preference, not renderer state) so every surface resolves the same gate; the
 * daemon combines it with its baked control-plane URL into cloudEnabled, which
 * is what reveals sign-in, cloud projects, and sandbox sessions.
 */
function CloudOfferingRow() {
	return null;
}`,
	"frontend/src/renderer/components/settings/HarnessSettingsSection.test.tsx": "const cloudEnabled = true;",
}

func TestGateNotesExplainHowTheTouchedUIIsReached(t *testing.T) {
	got := gateNotes(gateFixture, []string{"frontend/src/renderer/components/settings/HarnessSettingsSection.tsx"})
	for _, want := range []string{"### useCloudGate", "hooks/useCloudGate.ts:9", "export function useCloudGate(enabled = true): CloudGate {",
		"### cloudEnabled", "hooks/useCloudGate.ts:5", "> /** Whether the cloud offering is available", "> cloudEnabled: boolean;",
		"GeneralSettingsSection.tsx:2", "> * Developer Mode-only toggle for the cloud offering.", "> function CloudOfferingRow() {"} {
		if !strings.Contains(got, want) {
			t.Errorf("gate notes lack %q:\n%s", want, got)
		}
	}
	// a local variable nobody explains is not a gate, and tests are not read
	for _, unwanted := range []string{"isActive", "isInstalled", "test.tsx"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("gate notes have %q:\n%s", unwanted, got)
		}
	}
	if gateNotes(gateFixture, nil) != "" || uiFiles(intakeData{pr: pullRequest{Files: []struct {
		Path                 string
		Additions, Deletions int
	}{{Path: "backend/internal/cli/testing.go"}, {Path: "frontend/src/renderer/a.test.tsx"}, {Path: "frontend/src/renderer/styles.css"}}}}) != nil {
		t.Error("a PR with no UI source file has gate notes")
	}
}

func TestGateNotesCapAtEightAndPutOutrightGatesFirst(t *testing.T) {
	var src, hooks strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&src, "if (isShared%d() && flag%dEnabled) {}\n", i, i)
		fmt.Fprintf(&hooks, "export function isShared%d() {}\nconst flag%dEnabled = true;\n", i, i)
	}
	tree := mapTree{"app/ui/x.tsx": src.String(), "lib/gates.ts": hooks.String()}
	got := gateNotes(tree, []string{"app/ui/x.tsx"})
	if n := strings.Count(got, "### "); n != 8 || strings.Contains(got, "### isShared") {
		t.Fatalf("%d gates, want 8 outright ones:\n%s", n, got)
	}
}

func TestTestedSentencesQuoteHowTheAuthorTested(t *testing.T) {
	d := suggestFixture(t, "6266")
	got := testedSentences(d.pr.Body)
	if len(got) != 5 || got[0] != "Harness settings and logo tests: 51 passed." || !strings.Contains(got[1], "with the same fake daemon/cloud data.") {
		t.Fatalf("tested sentences %q", got)
	}
	if len(testedSentences("Adds a button.\n\n```\ntested locally in a fence.\n```\nNothing else.")) != 0 {
		t.Error("a code fence counts as the author's words")
	}
}

func TestContextSectionsSayWhereTheCodeIsAndStaySilentWithoutFacts(t *testing.T) {
	d := suggestFixture(t, "6266")
	d.headDir, d.baseDir = "/data/repos/o__r/headsha", "/data/repos/o__r/basesha"
	d.gates = "### cloudEnabled\n\n- a.ts:1\n> x\n"
	got := contextSections(d)
	for _, want := range []string{"## Where the code is", "- PR build checkout: /data/repos/o__r/headsha", "- Main checkout: /data/repos/o__r/basesha",
		"You may read any file there (Read, Grep, Glob) to learn how a feature is reached or gated; you cannot change them.", "## How the touched features are gated", "### cloudEnabled",
		"## How the author tested", `- "Harness settings and logo tests: 51 passed."`} {
		if !strings.Contains(got, want) {
			t.Errorf("sections lack %q:\n%s", want, got)
		}
	}
	if got := contextSections(intakeData{}); got != "" {
		t.Errorf("empty PR has sections %q", got)
	}
}

// TestContextSectionsOnTheRealPR6266 composes the three sections from the real PR build checkout of #6266. It needs
// that checkout, so it only runs with LITMUS_6266_CHECKOUT=<repos dir>/<head sha> (and LITMUS_6266_MAIN for the main one).
func TestContextSectionsOnTheRealPR6266(t *testing.T) {
	head := os.Getenv("LITMUS_6266_CHECKOUT")
	if head == "" {
		t.Skip("set LITMUS_6266_CHECKOUT to the #6266 head checkout to run")
	}
	svc, _ := newSvc(t)
	d := suggestFixture(t, "6266")
	d.pr.HeadRefOid = filepath.Base(head)
	d.headDir, d.baseDir = head, cmp.Or(os.Getenv("LITMUS_6266_MAIN"), filepath.Join(filepath.Dir(head), "main"))
	d.gates = svc.gateNotes(ctx, head, d)
	got := contextSections(d)
	t.Logf("\n%s", got)
	for _, want := range []string{"### useCloudGate", "### cloudEnabled", "Developer Mode-only toggle", "fake daemon/cloud data"} {
		if !strings.Contains(got, want) {
			t.Errorf("sections lack %q", want)
		}
	}
}
