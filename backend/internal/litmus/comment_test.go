package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeComment struct {
	ID          int64
	Login, Body string
}

// commentGH serves the comment endpoints from memory, records every argv, and reads each --input file when it is
// used. The listing comes back one JSON array per comment, as `gh --paginate` prints pages.
type commentGH struct {
	t             *testing.T
	login, author string
	private       string // "true" or "false": the repo's visibility
	comments      []fakeComment
	fail          string // an argv containing this fails
	notFound      int    // the next this many reads of an uploaded video answer 404
	calls, sent   []string
	argv          [][]string // every call's argv as gh got it
	uploads       int
}

const upload = "api --method POST -H Content-Type: video/mp4 https://uploads.github.com/"

func (g *commentGH) Run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil { // like gh, which is killed with its context
		return nil, err
	}
	line := strings.Join(args, " ")
	g.calls = append(g.calls, line)
	g.argv = append(g.argv, args)
	var in struct{ Body string }
	if i := slices.Index(args, "--input"); i >= 0 && !strings.HasPrefix(line, upload) { // a video upload sends a file, not a JSON body
		raw, err := os.ReadFile(args[i+1])
		if err != nil {
			g.t.Fatal(err)
		}
		g.sent = append(g.sent, string(raw))
		_ = json.Unmarshal(raw, &in)
	}
	if g.fail != "" && strings.Contains(line, g.fail) {
		return nil, errors.New("HTTP 403: Resource not accessible\nsecond line")
	}
	switch {
	case line == "api user --jq .login":
		return []byte(g.login + "\n"), nil
	case strings.HasPrefix(line, "repo view "):
		return []byte(g.private + "\n"), nil
	case line == "auth setup-git":
		return nil, nil
	case line == "api repos/o/r --jq .id":
		return []byte("1234\n"), nil
	case strings.HasPrefix(line, upload):
		g.uploads++
		return []byte(fmt.Sprintf(`{"url":"https://github.com/user-attachments/assets/%08d-aaaa-4bbb-8ccc-dddddddddddd"}`, g.uploads)), nil
	case strings.HasPrefix(line, "api -i https://github.com/user-attachments/assets/"):
		if g.notFound > 0 {
			g.notFound--
			return nil, errors.New("gh: Not Found (HTTP 404)")
		}
		return []byte("HTTP/2.0 200 OK\r\nContent-Length: 3\r\nContent-Type: video/mp4\r\n\r\nmp4"), nil
	case strings.HasPrefix(line, "pr view ") && strings.Contains(line, " --json author "):
		return []byte(g.author + "\n"), nil
	case strings.HasSuffix(line, "/comments --paginate"):
		out := "[]"
		if len(g.comments) > 0 {
			out = ""
		}
		for _, c := range g.comments {
			page, _ := json.Marshal([]map[string]any{{"id": c.ID, "body": c.Body, "user": map[string]string{"login": c.Login}}})
			out += string(page)
		}
		return []byte(out), nil
	case strings.HasPrefix(line, "api --method POST ") && strings.Contains(line, "/issues/") && strings.Contains(line, "/comments --input "):
		g.comments = append(g.comments, fakeComment{900, g.login, in.Body})
		return []byte(`{"id":900,"html_url":"https://github.com/o/r/pull/7#issuecomment-900"}`), nil
	case strings.HasPrefix(line, "api --method PATCH ") && strings.Contains(line, "/issues/comments/"):
		_, rest, _ := strings.Cut(line, "/issues/comments/")
		id := strings.Fields(rest)[0]
		return []byte(`{"html_url":"https://github.com/o/r/pull/7#issuecomment-` + id + `"}`), nil
	case strings.HasPrefix(line, "api --method POST ") && strings.Contains(line, "/reviews --input "):
		return []byte(`{"id":5}`), nil
	}
	g.t.Fatalf("unexpected gh call %q", line)
	return nil, nil
}

func (g *commentGH) has(prefix string) bool {
	return slices.ContainsFunc(g.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

var commentRun = Run{ID: "run_s1", SessionID: "s1", Repo: "o/r", PRNumber: 7, BaseSHA: "0ca80cfc370b21443c4a2b9d22199c047b8c74e7", HeadSHA: "e2204e0f361867a28310e71421a4227ab1d09544",
	State: RunSubmitted, CreatedAt: time.Date(2026, 10, 11, 8, 56, 0, 0, time.UTC), Comment: CommentPost}

// commentChecks: c1 passes on the head and fails on the base, c3 has no base result, c4 could not be reached.
func commentChecks() []Check {
	r := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg}} }
	return []Check{
		{ID: "c1", Name: "menu opens", Expect: "the menu stays open", State: CheckPass, Note: "Head: the menu stayed open and grew to 14 rows (ref @e12 | focus kept) <b> ![x](u) &commat;", Receipts: append(r("head"), r("head")...)},
		{ID: "c2", Name: "menu opens", Expect: "the menu stays open", State: CheckFail, Leg: "base", Note: "Base: closed the submenu.", Receipts: r("base")},
		{ID: "c3", Name: "last model", Expect: "the last model is remembered", State: CheckPass, Note: strings.Repeat("long note ", 20), Receipts: r("head")},
		{ID: "c4", Name: "effort slider", Expect: "the effort slider moves", State: CheckSkipped, Note: "needs a paid key"},
	}
}

func commentVerdict(checks []Check) Verdict {
	overall, reasons := derive(checks)
	return Verdict{Overall: overall, Checks: checks, Reasons: reasons}
}

// slop is what a comment must never say: our own words (legs, receipts, ids, engine refs, file names) and the
// machine-written tells. It is checked on every line but the image links and the marker.
var slop = regexp.MustCompile(`(?i)\b(legs?|heads?|base|receipts?|cuts?|mp4|run_\w+|c[0-9]+|@e[0-9]+|observables?|exercised|artifacts?|headline|snapshots?|refs?|ao session|additionally|ensuring|leverage|robust|seamless|not just)\b|\.(png|gif)\b|\x{2014}|[\x{201c}\x{201d}\x{2018}\x{2019}]`)

func noSlop(t *testing.T, body string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "![") && !strings.HasPrefix(line, "<!--") && slop.MatchString(line) {
			t.Errorf("%q in %q", slop.FindString(line), line)
		}
	}
}

func TestRenderCommentGolden(t *testing.T) {
	now := commentRun.CreatedAt.Add(4*time.Minute + 3*time.Second)
	got := renderComment(commentRun, commentVerdict(commentChecks()), evidence{}, now)
	const want = `Litmus tried #7 on the PR build and on main.

**Result: the fix works; fine to merge from my side. One check only ran on the PR build and one could not be done.**

I checked that the menu stays open. On the PR build, the menu stayed open and grew to 14 rows (that element \| focus kept) &lt;b> !\[x](u) &amp;commat;. On main, closed the submenu. That is the fix, and it holds.

Next I checked that the last model is remembered. On the PR build, long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note long note. I could not compare it with main.

I could not check that the effort slider moves. Needs a paid key.

Took 4 minutes.

<!-- litmus:comment o/r#7 -->`
	if got != want {
		t.Fatalf("comment:\n%s\nwant:\n%s", got, want)
	}
	noSlop(t, got)
}

func TestRenderCommentFitsTheCapAndClipsTableRowsLast(t *testing.T) {
	long := strings.Repeat("n", 300)
	var checks []Check
	for i := 1; i <= 6; i++ {
		checks = append(checks, Check{ID: fmt.Sprint("c", i), Name: "check " + fmt.Sprint(i), State: CheckPartial, Note: long, Receipts: []Receipt{{Seq: i}}})
	}
	v := commentVerdict(checks)
	for i := 0; i < 30; i++ { // far more Limits text than 6,000 characters
		v.Reasons = append(v.Reasons, fmt.Sprintf("reason %d %s", i, long))
	}
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if len(got) > commentCap || !strings.HasSuffix(got, "\n"+commentMarker("o/r", 7)) || !strings.HasPrefix(got, "Litmus tried #7 on the PR build.\n\n**Result: nothing failed, but I could not confirm the change from the app; read the diff before merging.**\n") {
		t.Fatalf("len %d, ends %q", len(got), got[len(got)-60:])
	}
	rows := 0
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "| check ") && strings.HasSuffix(l, strings.Repeat("n", 80)+"... |") {
			rows++
		}
	}
	if rows != 6 { // the Limits gave way; every table note kept its full clip
		t.Fatalf("%d of 6 table notes kept the full clip:\n%s", rows, got)
	}
	if !strings.Contains(got, "\nAnd ") || strings.Contains(got, "AO session") {
		t.Fatalf("squeezed Limits do not say so:\n%s", got)
	}

	for i := 0; i < 300; i++ { // nothing fits: the body is cut, the marker survives
		checks = append(checks, checks[0])
	}
	got = renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt)
	if len(got) > commentCap || !strings.HasSuffix(got, commentMarker("o/r", 7)) {
		t.Fatalf("hard cap: len %d", len(got))
	}
}

func TestBlockedCommentCarriesTheReason(t *testing.T) {
	got := renderComment(commentRun, Verdict{Overall: CheckBlocked, Reasons: []string{"prepare refused: no checkout"}}, evidence{}, commentRun.CreatedAt)
	if !strings.HasPrefix(got, "Litmus tried #7 on the PR build.\n\n**Result: could not test this one.**\n") || !strings.Contains(got, "\nI could not run the checks. Prepare refused: no checkout.\n") || strings.Contains(got, "| what was checked |") {
		t.Fatalf("blocked comment:\n%s", got)
	}
}

func TestUpsertEditsOnlyTheTokensOwnMarkerComment(t *testing.T) {
	marker := commentMarker("o/r", 7)
	for name, c := range map[string]struct {
		comments []fakeComment
		method   string // of the write, with its path
		state    string
	}{
		"own marker comment on a later page": {[]fakeComment{{1, "alice", "hi"}, {22, "Litmus-Bot", "old\n" + marker}}, "PATCH repos/o/r/issues/comments/22", "updated"},
		"no marker comment":                  {[]fakeComment{{1, "alice", "hi"}}, "POST repos/o/r/issues/7/comments", "posted"},
		"marker by another author":           {[]fakeComment{{3, "alice", "quoting " + marker}}, "POST repos/o/r/issues/7/comments", "posted"},
		"marker of another PR":               {[]fakeComment{{4, "litmus-bot", commentMarker("o/r", 70)}}, "POST repos/o/r/issues/7/comments", "posted"},
	} {
		t.Run(name, func(t *testing.T) {
			g := &commentGH{t: t, login: "litmus-bot", comments: c.comments}
			svc := New(NewStore(t.TempDir()), nil).WithGH(g)
			is(t, "")(nil, svc.store.Save(commentRun))
			url, state, err := svc.upsertComment(ctx, commentRun, CheckPass, "BODY "+marker)
			if err != nil || state != c.state || !strings.HasPrefix(url, "https://github.com/o/r/pull/7#issuecomment-") {
				t.Fatalf("url %q state %q err %v", url, state, err)
			}
			want := []string{"api user --jq .login", "api repos/o/r/issues/7/comments --paginate", "api --method " + c.method + " --input "}
			for i, w := range want {
				if !strings.HasPrefix(g.calls[i], w) {
					t.Fatalf("call %d = %q, want prefix %q", i, g.calls[i], w)
				}
			}
			if len(g.sent) != 1 || g.field(0, "body") != "BODY "+marker || slices.ContainsFunc(g.calls, func(c string) bool { return strings.Contains(c, "BODY") }) {
				t.Fatalf("the body must travel in the --input file only: sent %q, calls %q", g.sent, g.calls)
			}
		})
	}
}

func TestSecondSubmitEditsTheComment(t *testing.T) {
	g := &commentGH{t: t, login: "litmus-bot"}
	svc := New(NewStore(t.TempDir()), nil).WithGH(g)
	is(t, "")(nil, svc.store.Save(commentRun))
	_, first, _ := svc.upsertComment(ctx, commentRun, CheckPass, "one "+commentMarker("o/r", 7))
	_, second, err := svc.upsertComment(ctx, commentRun, CheckPass, "two "+commentMarker("o/r", 7))
	if err != nil || first != "posted" || second != "updated" || !g.has("api --method PATCH repos/o/r/issues/comments/900 ") || len(g.comments) != 1 {
		t.Fatalf("first %q second %q err %v calls %q", first, second, err, g.calls)
	}
	if n := strings.Count(strings.Join(g.calls, "\n"), "api user"); n != 1 {
		t.Fatalf("the login was asked %d times, want once", n)
	}
}

func TestCommentReviewOnlyForFailByAnotherAuthor(t *testing.T) {
	for _, c := range []struct {
		overall CheckState
		author  string
		review  bool
	}{{CheckFail, "alice", true}, {CheckFail, "Litmus-Bot", false}, {CheckPass, "alice", false}, {CheckPartial, "alice", false}} {
		g := &commentGH{t: t, login: "litmus-bot", author: c.author}
		svc := New(NewStore(t.TempDir()), nil).WithGH(g)
		is(t, "")(nil, svc.store.Save(commentRun))
		if _, _, err := svc.upsertComment(ctx, commentRun, c.overall, "b"); err != nil {
			t.Fatal(err)
		}
		if got := g.has("api --method POST repos/o/r/pulls/7/reviews "); got != c.review {
			t.Errorf("%s by %s: review %t, want %t (%q)", c.overall, c.author, got, c.review, g.calls)
		}
		if c.review && (g.field(1, "event") != "REQUEST_CHANGES" || g.field(1, "body") != "Litmus found a failing check on this pull request.\nDetails: https://github.com/o/r/pull/7#issuecomment-900") {
			t.Errorf("review: %s", g.sent[1])
		}
	}
	g := &commentGH{t: t, login: "litmus-bot", author: "alice", fail: "/reviews"} // a refused review leaves the comment standing
	svc := New(NewStore(t.TempDir()), nil).WithGH(g)
	is(t, "")(nil, svc.store.Save(commentRun))
	if url, state, err := svc.upsertComment(ctx, commentRun, CheckFail, "b"); err != nil || state != "posted" || url == "" {
		t.Fatalf("url %q state %q err %v", url, state, err)
	}
	if log, _ := os.ReadFile(filepath.Join(svc.store.root, "run_s1", "comment.log")); !strings.Contains(string(log), "review not submitted") {
		t.Fatalf("comment.log = %q", log)
	}
}

func TestSubmitCommentModesAndFailure(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t) // checks: c1 head pass, c2 base fail, c3 head pass: a PASS
	g := &commentGH{t: t, login: "litmus-bot"}
	svc.gh = g
	v, err := svc.Submit(ctx, "s1", "", "")
	run, _ := svc.store.Get("run_s1")
	body, _ := os.ReadFile(filepath.Join(runDir, "comment.md"))
	if err != nil || v.CommentState != "posted" || run.CommentState != "posted" || run.CommentURL != v.CommentURL || v.CommentURL == "" {
		t.Fatalf("posted: verdict %+v run %+v err %v", v, run, err)
	}
	if g.field(0, "body") != string(body) || !strings.HasPrefix(string(body), "Litmus tried #6394 on the PR build and on main.\n\n**Result: the fix works") {
		t.Fatalf("comment.md is not the posted body: %s", body)
	}

	for name, c := range map[string]struct {
		mode  string
		fail  string
		state string
	}{
		"off":         {CommentOff, "", "off"},
		"older run":   {"", "", "off"}, // a run from before comments existed never posts
		"login fails": {CommentPost, "api user", "failed:HTTP 403: Resource not accessible"},
		"list fails":  {CommentPost, "--paginate", "failed:HTTP 403: Resource not accessible"},
		"write fails": {CommentPost, "--method POST", "failed:HTTP 403: Resource not accessible"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			g := &commentGH{t: t, login: "litmus-bot", fail: c.fail}
			svc.gh = g
			_, err := svc.update("run_s1", func(r *Run) { r.Comment = c.mode })
			is(t, "")(nil, err)
			v, err := svc.Submit(ctx, "s1", "", "") // a failed post is not a failed submit
			run, _ := svc.store.Get("run_s1")
			if err != nil || v.CommentState != c.state || run.CommentState != c.state || run.State != RunSubmitted || v.CommentURL != "" {
				t.Fatalf("verdict %+v run %+v err %v", v, run, err)
			}
			exists(t, filepath.Join(runDir, "comment.md"))
			log, _ := os.ReadFile(filepath.Join(runDir, "comment.log"))
			log = regexp.MustCompile(`(?m)^\S+ pruned: .*\n`).ReplaceAll(log, nil) // the retention pass logs there too
			if (c.fail == "") != (len(g.calls) == 0 && len(log) == 0) || (c.fail != "" && !strings.Contains(string(log), "Resource not accessible")) {
				t.Fatalf("off must touch neither gh nor the log; failed must log: calls %q log %q", g.calls, log)
			}
		})
	}

	blocked, _, runDir, _ := submitSvc(t)
	bg := &commentGH{t: t, login: "litmus-bot"}
	blocked.gh = bg
	v, err = blocked.SubmitBlocked(ctx, "s1", "prepare refused")
	if body, _ = os.ReadFile(filepath.Join(runDir, "comment.md")); err != nil || v.CommentState != "off" || len(bg.calls) != 0 || !strings.Contains(string(body), "**Result: could not test this one.**") {
		t.Fatalf("blocked: %+v %v\n%s", v, err, body)
	}
}

func TestStartNoCommentSetsTheRunsMode(t *testing.T) {
	ids := []string{"s8", "s9"}
	svc := New(NewStore(t.TempDir()), nil).WithGH(fixtureGH(t)).WithSpawner(func(context.Context, SpawnRequest) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	for _, c := range []struct{ session, mode string }{{"s8", CommentPost}, {"s9", CommentOff}} {
		started, err := svc.Start(ctx, StartRequest{PRURL: prURL, NoComment: c.mode == CommentOff})
		run, ierr := svc.Intake(ctx, started.SessionID, prURL)
		if err != nil || ierr != nil || started.SessionID != c.session || run.Comment != c.mode {
			t.Fatalf("%s: started %+v run %+v errs %v %v", c.session, started, run, err, ierr)
		}
	}
}

// field is one string of the i-th request body that was sent.
func (g *commentGH) field(i int, name string) string {
	var m map[string]string
	_ = json.Unmarshal([]byte(g.sent[i]), &m)
	return m[name]
}

func TestMdTextDoublesBackslashes(t *testing.T) {
	// a trailing backslash must not escape the pipe that cell() adds, nor a note's own backslash the markup after it
	if got, want := mdText(`a\ | b\[x]`, 50), `a\\ \| b\\\[x]`; got != want {
		t.Fatalf("mdText = %q, want %q", got, want)
	}
}

func TestVerdictLineSentences(t *testing.T) {
	counted := count(commentChecks()) // passed 2, one shown against main, one not compared, one unreached
	for _, c := range []struct {
		overall CheckState
		t       tally
		want    string
	}{
		{CheckPass, counted, "the fix works; fine to merge from my side. One check only ran on the PR build and one could not be done"},
		{CheckPass, tally{passed: 3, shown: 1, notCompared: 2, unreached: 3}, "the fix works; fine to merge from my side. A few checks only ran on the PR build and a few could not be done"},
		{CheckPass, tally{passed: 1, shown: 1, unreached: 1}, "the fix works; fine to merge from my side. One check could not be done"},
		{CheckPass, tally{passed: 1, shown: 1, unreached: 2}, "the fix works; fine to merge from my side. A few checks could not be done"},
		{CheckPass, tally{passed: 3, shown: 1, notCompared: 2}, "the fix works; fine to merge from my side. A few checks only ran on the PR build"},
		{CheckPass, tally{passed: 1, shown: 1, notCompared: 1}, "the fix works; fine to merge from my side. One check only ran on the PR build"},
		{CheckPass, tally{passed: 1, shown: 1}, "the PR does what it says; fine to merge from my side"},
		{CheckPass, tally{passed: 2, pre: 1}, "the PR does what it says; fine to merge from my side"},
		{CheckPartial, tally{passed: 2}, "the PR build works, but nothing differed from main; read the diff before merging"},
		{CheckPartial, tally{partial: 2}, "nothing failed, but I could not confirm the change from the app; read the diff before merging"},
		{CheckPartial, tally{pre: 1}, "nothing failed, but I could not confirm the change from the app; read the diff before merging"},
		{CheckPartial, tally{unreached: 4}, "could not test this from the app"},
		{CheckPartial, tally{unreached: 1, partial: 1}, "nothing failed, but I could not confirm the change from the app; read the diff before merging"},
		{CheckFail, tally{failed: 1, passed: 2}, "something broke; do not merge yet"},
		{CheckFail, tally{failed: 3}, "something broke; do not merge yet"},
		{CheckBlocked, tally{}, "could not test this one"},
	} {
		if got := verdictLine(c.overall, c.t, "main"); got != c.want {
			t.Errorf("%s %+v:\n got %q\nwant %q", c.overall, c.t, got, c.want)
		}
	}
	if got := verdictLine(CheckPartial, tally{passed: 1}, "develop"); got != "the PR build works, but nothing differed from develop; read the diff before merging" {
		t.Errorf("a run against another base branch names it: %q", got)
	}
}

func TestAreaLineSaysWhichChangedAreasTheChecksCover(t *testing.T) {
	// run 11: three e2e specs, two unit tests and one component. The component's file name shares the stem "nav" with
	// "navigate", and it is the only area that is not tests, so it counts as covered either way.
	pr := []Area{{Name: "Tests", Files: 5}, {Name: "Renderer components", Files: 1, Stems: []string{"TitlebarNav"}}}
	navigate := []Check{{Name: "history", Expect: "back and forward navigate", Note: "the arrows stayed"}}
	if got, want := areaLine(pr, navigate), "What the PR touches. Tests, 5 files, not run here. The renderer component TitlebarNav, covered above."; got != want {
		t.Fatalf("line %q, want %q", got, want)
	}
	if got, want := areaLine(pr, []Check{{Name: "colors", Expect: "the page is red"}}), "What the PR touches. Tests, 5 files, not run here. The renderer component TitlebarNav, covered above."; got != want {
		t.Fatalf("the only area besides tests is about what the checks claim: %q, want %q", got, want)
	}
	checks := []Check{{Name: "history", Expect: "going back works", Note: "the sidebar shows the arrows"}, {Name: "x", Note: "the latest build happens often; Project files changed"}}
	areas := []Area{{Name: "Shell sidebar", Files: 3, Stems: []string{"Sidebar", "useHistory"}}, {Name: "Tests", Files: 1}, {Name: "Project files", Files: 2, Stems: []string{"package", "lockfile"}}, {Name: "App", Files: 4, Stems: []string{"index"}}}
	want := "What the PR touches. Shell sidebar, 3 files, covered above. Tests, 1 file, not run here. Project files, 2 files, not touched by this run. App, 4 files, not touched by this run."
	if got := areaLine(areas, checks); got != want { // a file word must start a word the checks use; "index" and "use" are too common to count
		t.Fatalf("line %q, want %q", got, want)
	}
	if got, want := areaLine([]Area{{Name: "Shell sidebar", Files: 2}, {Name: "Chat", Files: 1}}, checks), "What the PR touches. Shell sidebar, 2 files, covered above. Chat, 1 file, not touched by this run."; got != want { // older runs kept no file names: the area's own words stand in
		t.Fatalf("older run: %q, want %q", got, want)
	}
	if got := areaLine(nil, checks); got != "" {
		t.Fatalf("no areas, no line: %q", got)
	}
	var many []Area
	for i := 1; i <= 8; i++ {
		many = append(many, Area{Name: fmt.Sprint("Area", i), Files: i})
	}
	if got := areaLine(many[:7], nil); !strings.HasSuffix(got, " Area6, 6 files, not touched by this run. And 1 more area.") || strings.Contains(got, "Area7") {
		t.Fatalf("seven areas: %q", got)
	}
	if got := areaLine(many, nil); !strings.HasSuffix(got, " And 2 more areas.") {
		t.Fatalf("eight areas: %q", got)
	}
}

func TestClauseCutsAtAWordAndHasNoStopOfItsOwn(t *testing.T) {
	long := "With sidebar expanded and brand shown, Collapse, Go back and Go forward buttons are all visible"
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{long, 160, long},
		{"Ends with a stop...", 160, "Ends with a stop"},
		{strings.Repeat("alpha ", 40), 160, strings.TrimSpace(strings.Repeat("alpha ", 26))},
		{"it shows Go back and the", 20, "it shows Go back"},                           // a cut never ends on "and" or "the" // the 160th character is inside a word
		{"alpha beta gamma delta", 16, "alpha beta gamma"},                             // the cut falls on a space: no word is lost
		{"keep (open bracket and then a long tail", 20, "keep"},                        // an unclosed bracket goes with the cut
		{"keep (both closed) and more words after", 30, "keep (both closed) and more"}, //
	} {
		if got := clause(c.in, c.n); got != c.want {
			t.Errorf("clause(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestCommentSaysWholeSentencesInTheReadersWords(t *testing.T) {
	long := "With sidebar expanded and brand shown, Collapse, Go back and Go forward buttons are all visible"
	over := strings.Repeat("the sidebar keeps every button ", 8) + "and then some"
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg}} }
	checks := []Check{
		{ID: "c1", Name: "history-visible", Expect: long, State: CheckPass, Note: "Head shows the arrows", Receipts: rc("head")},
		{ID: "c2", Name: "history-visible", Expect: long, State: CheckFail, Leg: "base", Note: "Base: expanded sidebar shows only Collapse", Receipts: rc("base")},
		{ID: "c3", Name: "history-navigates", Expect: "Back/forward navigate and stay visible", State: CheckPartial, Note: "Forward did not land. C2 needs the arrows to navigate, so it cannot run there (see c1).", Receipts: rc("head")},
		{ID: "c4", Name: "long", Expect: over, State: CheckPass, Note: "fine", Receipts: rc("head")},
	}
	v := commentVerdict(checks)
	v.SkipBase = "C2 has no arrows; base has none when expanded, head is slow"
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	for _, want := range []string{
		"I checked that with sidebar expanded and brand shown, Collapse, Go back and Go forward buttons are all visible. The PR build shows the arrows. On main, expanded sidebar shows only Collapse.",
		"Next I checked that Back/forward navigate and stay visible. On the PR build, forward did not land. History visible needs the arrows to navigate, so it cannot run there (see history visible).\n",
		"\n\nI did not run main: history visible has no arrows; main has none when expanded, the PR build is slow.\n\n",
		"I also checked that " + strings.TrimSuffix(strings.Repeat("the sidebar keeps every button ", 5), " ") + ". On the PR build, fine.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "....") || strings.Contains(got, "Worth a look") || strings.Contains(got, "could not compare this") || strings.Count(got, "I did not run") != 1 {
		t.Errorf("clipped sentence, the old closing line, or the skip reason not said exactly once:\n%s", got)
	}
	noSlop(t, got)
}

func TestHeroCaptionSaysWhatEachSideShowedInNinetyCharacters(t *testing.T) {
	both := hero{pr: &shot{}, base: &shot{}}
	if got := heroCaption(commentRun, both); got != "Main on the left, PR build on the right." {
		t.Errorf("no notes: %q", got)
	}
	both.baseNote, both.prNote = "No Go back or Go forward buttons", "Expanded sidebar shows Collapse, Go back, Go forward (both disabled on Home) and Orchestrator.inc brand; still present after toggling a project row"
	want := "Main on the left: no Go back or Go forward buttons. PR build on the right: expanded sidebar shows Collapse, Go back, Go forward (both disabled on Home)."
	if got := heroCaption(commentRun, both); got != want {
		t.Errorf("caption %q, want %q", got, want)
	}
	both.baseNote = ""
	if got := heroCaption(commentRun, both); got != "Main on the left, PR build on the right." {
		t.Errorf("one note missing: %q", got)
	}
}

func TestPlainTesterRewritesTheTestersVocabulary(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "history-visible"}, {ID: "c3", Name: "menu opens"}}
	run := commentRun
	for in, want := range map[string]string{
		"Head: both arrows show":                            "both arrows show",
		"head leg shows it, the headline and database stay": "the PR build leg shows it, the headline and database stay",
		"Base has none; BASE too":                           "Main has none; Main too",
		"C1 needs it, shown by c3, and c9 too":              "History visible needs it, shown by menu opens, and that check too",
		"nothing to change here":                            "nothing to change here",
	} {
		if got := plainTester(in, run, checks); got != want {
			t.Errorf("plainTester(%q) = %q, want %q", in, got, want)
		}
	}
	run.BaseRef = "develop"
	if got := plainTester("base differs", run, checks); got != "develop differs" {
		t.Errorf("another base branch is named: %q", got)
	}
}

func TestChecksTableAppearsOnlyBeyondFourChecks(t *testing.T) {
	var checks []Check
	for i := 1; i <= 5; i++ {
		checks = append(checks, Check{ID: fmt.Sprint("c", i), Name: fmt.Sprint("check ", i), Expect: fmt.Sprint("thing ", i, " works"), State: CheckPass, Note: "fine", Receipts: []Receipt{{Seq: i}}})
	}
	const header = "| what was checked | PR build | main | note |"
	if got := renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt); !strings.Contains(got, "\n\n"+header+"\n|---|---|---|---|\n| thing 1 works | works | not compared | fine |\n") || strings.Count(got, "checked that thing") != 5 {
		t.Fatalf("five checks get a table and a paragraph each:\n%s", got)
	}
	if got := renderComment(commentRun, commentVerdict(checks[:4]), evidence{}, commentRun.CreatedAt); strings.Contains(got, header) {
		t.Fatalf("four checks read as paragraphs:\n%s", got)
	}
}

func TestPlainTextKeepsQuestionsAndEmailAddresses(t *testing.T) {
	if got := plainSentence("does it save?", 80); got != "does it save?" {
		t.Errorf("question: %q", got)
	}
	if got, want := mdText("mail a@e2e.com, ref @e12 and @e7", 80), "mail a@\u200be2e.com, that element and that element"; got != want {
		t.Errorf("refs: %q, want %q", got, want)
	}
}

func TestCommentFirstLineNamesTheClosedIssues(t *testing.T) {
	url := func(n int) string { return fmt.Sprintf("https://github.com/o/r/issues/%d", n) }
	run := commentRun
	run.PRNumber = 6403
	for _, c := range []struct {
		issues []IssueRef
		want   string
	}{
		{nil, "Litmus tried #6403 on the PR build and on main."},
		{[]IssueRef{{Number: 6390, URL: url(6390)}}, "Litmus tried #6403 (for [#6390](" + url(6390) + ")) on the PR build and on main."},
		{[]IssueRef{{Number: 6390, URL: url(6390)}, {Number: 6391, URL: url(6391)}}, "Litmus tried #6403 (for [#6390](" + url(6390) + ") and [#6391](" + url(6391) + ")) on the PR build and on main."},
		{[]IssueRef{{Number: 1}, {Number: 2}, {Number: 3}}, "Litmus tried #6403 (for #1, #2 and #3) on the PR build and on main."},
	} {
		run.Issues = c.issues
		got := renderComment(run, commentVerdict(commentChecks()), evidence{}, run.CreatedAt.Add(time.Minute))
		if first, _, _ := strings.Cut(got, "\n"); first != c.want {
			t.Errorf("first line %q, want %q", first, c.want)
		}
		noSlop(t, got)
	}
	run.Issues = []IssueRef{{Number: 6390, URL: url(6390)}}
	one := plainVerdict(run, commentVerdict(commentChecks()))
	for i := range one.Checks {
		one.Checks[i].Leg = "head"
		one.Checks[i].Receipts = nil
	}
	if first, _, _ := strings.Cut(commentBody(run, one, evidence{}, run.CreatedAt, commentFits[0]), "\n"); first != "Litmus tried #6403 (for [#6390]("+url(6390)+")) on the PR build." {
		t.Errorf("head-only first line %q", first)
	}
}
