package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

var (
	prTitle = regexp.MustCompile(`(?m)^- #\d+ (.+)$`) // the PR line of context-pack.md
	// noChange matches the conventional-commit types of PRs that claim no behavior change.
	noChange = regexp.MustCompile(`(?i)^(docs|chore|tests?|ci|build|style)(\([^)]*\))?!?:`)
)

// claimsChange reports whether the PR says it changes behavior: its title is not a docs, chore, test, ci,
// build or style commit. A run without a context pack claims nothing.
func claimsChange(runDir string) bool {
	pack, _ := os.ReadFile(filepath.Join(runDir, "context-pack.md"))
	m := prTitle.FindSubmatch(pack)
	return len(m) > 1 && !noChange.Match(m[1])
}

// baseResult is what the base leg showed for a head check: the outcome of a base-leg check of the same
// name, "observed" when only base receipts exist, or "" when the base was never looked at.
func baseResult(checks []Check, c Check) string {
	for _, b := range checks {
		if b.onLeg() == "base" && strings.EqualFold(b.Name, c.Name) && slices.Contains(outcomes, b.State) {
			return string(b.State)
		}
	}
	if slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == "base" }) {
		return "observed"
	}
	return ""
}

// preExisting reports whether a head check that failed or was partial did the same on the base: the PR did not cause it.
func preExisting(checks []Check, c Check) bool {
	b := baseResult(checks, c)
	return c.onLeg() == "head" && (c.State == CheckFail || c.State == CheckPartial) && (b == string(CheckFail) || b == string(CheckPartial))
}

// headlineRule is the rule derive applies, as verdict.md states it (the result is the overall verdict).
const headlineRule = "The result is fail if any head check failed. It is pass if every head check passed and at least one passed on the head while failing on the base, which shows the difference. Otherwise it is partial. Checks the tester could not reach are left out, base-only checks are context, and a head check that failed or was partial the same way on the base is pre-existing and left out."

// The reasons derive gives for a partial with nothing worse; the comment says them in its own words.
const (
	reasonPreOnly      = "every head check that did not pass also did not pass on the base"
	reasonPrePartial   = "every head check that was partial was partial on the base too"
	reasonPreBoth      = "every head check that did not pass also did not pass on the base, failing or partial"
	reasonNoneReached  = "no head check could be reached"
	reasonNoDifference = "no check passed on the head while failing on the base, so the base-vs-head difference was not shown"
)

// derive folds the head checks into the overall outcome by headlineRule. A check that fails on the base is
// usually the bug the PR fixes, so a head pass beside a base fail is the evidence a pass needs.
func derive(checks []Check) (overall CheckState, reasons []string) {
	failed, allPass, differs, reached, preFail, prePartial := false, true, false, false, false, false
	for _, c := range checks {
		if c.onLeg() != "head" || c.State == CheckSkipped {
			continue
		}
		if preExisting(checks, c) {
			preFail, prePartial = preFail || c.State == CheckFail, prePartial || c.State == CheckPartial
			continue
		}
		if c.State != CheckPass && c.Note != "" {
			reasons = append(reasons, c.ID+" "+c.Name+": "+c.Note)
		}
		reached = true
		failed, allPass = failed || c.State == CheckFail, allPass && c.State == CheckPass
		differs = differs || (c.State == CheckPass && baseResult(checks, c) == string(CheckFail))
	}
	switch {
	case failed:
		return CheckFail, reasons
	case allPass && differs:
		return CheckPass, reasons
	case !reached && preFail && prePartial:
		reasons = append(reasons, reasonPreBoth)
	case !reached && prePartial:
		reasons = append(reasons, reasonPrePartial)
	case !reached && preFail:
		reasons = append(reasons, reasonPreOnly)
	case !reached:
		reasons = append(reasons, reasonNoneReached)
	case allPass:
		reasons = append(reasons, reasonNoDifference)
	}
	return CheckPartial, reasons
}

// Submit validates the evidence, closes the video, derives the overall outcome, writes verdict.md and
// copies the evidence into the session's artifact dir. Every check needs an outcome and, unless skipped, a receipt;
// otherwise nothing is written and the offenders are listed.
//
// A prepared base that was never launched refuses the submit (TESTING_BASE_NOT_RUN) while at least baseGuardLeft of the
// deadline remains, unless skipBase carries the reason the tester gave for not running it ("" means no skip).
func (s *Service) Submit(ctx context.Context, sessionID, summary, skipBase string) (Verdict, error) {
	skip := strings.Join(strings.Fields(skipBase), " ")
	if skipBase != "" && skip == "" {
		return Verdict{}, invalid("TESTING_REASON_REQUIRED", "--skip-base needs a non-empty --reason")
	}
	run, done, err := s.begin(sessionID, "submit") // a retry after a CLI timeout gets TESTING_BUSY
	if err != nil {
		return Verdict{}, err
	}
	defer done()
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Verdict{}, err
	}
	if len(checks) == 0 {
		return Verdict{}, invalid("TESTING_NO_CHECKS", "submit needs at least one check")
	}
	if err := forAuthor(skip); err != nil {
		return Verdict{}, err
	}
	var incomplete []string // no receipt, or never ended
	for _, c := range checks {
		if (len(c.Receipts) == 0 && c.State != CheckSkipped) || !slices.Contains(outcomes, c.State) {
			incomplete = append(incomplete, c.ID+" "+c.Name)
		}
	}
	if len(incomplete) > 0 {
		return Verdict{}, apierr.Invalid("TESTING_CHECKS_INCOMPLETE", "every check needs an outcome and a receipt (a skipped check needs none); incomplete: "+strings.Join(incomplete, "; "), map[string]any{"checks": incomplete})
	}
	if skip == "" && s.baseFailed(run) {
		return Verdict{}, conflict("TESTING_BASE_NOT_RUN", run.baseName()+" was prepared but failed to launch, so there are no base results; submit with `--skip-base --reason \"<say that "+run.baseName()+" failed to launch and why>\"`")
	}
	if (skip == "" && s.baseNeverLaunched(run, checks)) || (skip != "" && !s.maySkipBase(run)) {
		how := "run " + run.baseName() + " (`ao testing launch base`), repeat every check there"
		if testsOnly(checks) { // nothing on screen to repeat: the tests are enough
			how = "run the same tests on " + run.baseName() + " (`ao testing test --leg base`)"
		}
		return Verdict{}, conflict("TESTING_BASE_NOT_RUN", how+" and record a result for each: pass, fail, partial, or skipped with a note saying why. --skip-base is only for a main that will not build or launch, or when under 4 minutes remain")
	}
	if skip == "" {
		if ids := s.notComparedOnBase(run, checks); len(ids) > 0 {
			return Verdict{}, conflict("TESTING_BASE_INCOMPLETE", baseMissing(run, ids))
		}
	}
	return s.closeRun(ctx, run, checks, summary, "", skip)
}

// baseGuardLeft is how much of the deadline must remain for an unlaunched base to refuse a submit.
const baseGuardLeft = 4 * time.Minute

// maySkipBase is true when --skip-base is honoured: Litmus saw the base fail to launch (or never got to prepare it), or
// less than baseGuardLeft of the deadline remains. A tester's own view that a comparison would not matter is not a reason.
func (s *Service) maySkipBase(run Run) bool {
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == "base" })
	return i < 0 || run.Legs[i].State == "failed" || run.Deadline.Sub(s.clock()) < baseGuardLeft
}

// baseNeverLaunched is true for a run whose base leg was prepared but not launched and that still has time to launch it.
// A run whose every receipt is a test needs no window: a test result on the base counts as the base having been run.
func (s *Service) baseNeverLaunched(run Run, checks []Check) bool {
	if testsOnly(checks) && slices.ContainsFunc(checks, func(c Check) bool { return c.onLeg() == "base" && len(c.Receipts) > 0 }) {
		return false
	}
	return slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "prepared" }) && run.Deadline.Sub(s.clock()) >= baseGuardLeft
}

// testsOnly is true when the checks hold receipts and every one is a test run: no verb drove a window.
func testsOnly(checks []Check) bool {
	seen := false
	for _, c := range checks {
		for _, r := range c.Receipts {
			if r.Action != "test" {
				return false
			}
			seen = true
		}
	}
	return seen
}

// baseFailed is true for a run whose base leg launched and then failed.
func (s *Service) baseFailed(run Run) bool {
	return slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "failed" })
}

// baseMissing tells the tester how to give the head checks ids a result on the base.
func baseMissing(run Run, ids []string) string {
	main, who := run.baseName(), "check "+ids[0]+" has"
	if len(ids) > 1 {
		who = "checks " + strings.Join(ids, ", ") + " have"
	}
	return who + " no result on " + main + "; repeat it there or record it with --result skipped and a note saying why"
}

// notComparedOnBase lists the head checks the tester could reach that have no base result of any kind (a base check
// ended skipped, with a note saying why, counts), once the base launched (or, for a run of tests alone, was prepared)
// and while at least baseGuardLeft of the deadline remains: the comment says what each check showed on the base or why it did not.
func (s *Service) notComparedOnBase(run Run, checks []Check) (ids []string) {
	ran := func(l Leg) bool {
		return l.Name == "base" && (l.State == "running" || (l.State == "prepared" && testsOnly(checks)))
	}
	if run.Deadline.Sub(s.clock()) < baseGuardLeft || !slices.ContainsFunc(run.Legs, ran) {
		return nil
	}
	for _, c := range checks {
		if c.onLeg() == "head" && c.State != CheckSkipped && (baseResult(checks, c) == "" || baseSkippedWithoutNote(checks, c)) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// baseSkippedWithoutNote is true when a head check's only base result is a skip that says nothing about why.
func baseSkippedWithoutNote(checks []Check, c Check) bool {
	i := baseTwin(checks, c)
	return i >= 0 && checks[i].State == CheckSkipped && strings.TrimSpace(checks[i].Note) == ""
}

// SubmitBlocked closes the run as blocked: the tester hit an error it cannot fix. No receipts are
// required; verdict.md carries the reason and whatever evidence the run has.
func (s *Service) SubmitBlocked(ctx context.Context, sessionID, reason string) (Verdict, error) {
	if strings.TrimSpace(reason) == "" {
		return Verdict{}, invalid("TESTING_REASON_REQUIRED", "a blocked submit needs a non-empty reason")
	}
	run, done, err := s.begin(sessionID, "submit")
	if err != nil {
		return Verdict{}, err
	}
	defer done()
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Verdict{}, err
	}
	return s.closeRun(ctx, run, checks, "", strings.TrimSpace(reason), "")
}

// closeRun turns the checks into a verdict (overall blocked when blocked is the reason), writes verdict.md,
// publishes the evidence, marks the run submitted and reports the session done. A run of a project that asks before
// posting stops at awaiting-approval instead, with comment.md written and nothing published or posted. skipBase is
// the reason the tester gave for not running the base, and lands in Limits.
func (s *Service) closeRun(ctx context.Context, run Run, checks []Check, summary, blocked, skipBase string) (Verdict, error) {
	runDir := filepath.Join(s.store.root, run.ID)
	run = s.stopRecording(ctx, run) // while the engines are still alive; returns the run with its recording state
	videos := s.finishVideo(ctx, runDir, run)
	var limits []string
	cleanErr := s.teardown(ctx, run) // every close cleans up after itself; a failure is reported, never fatal
	if cleanErr != nil {
		slog.Warn("litmus: cleanup after submit failed", "run", run.ID, "error", cleanErr)
		limits = append(limits, "Cleanup after submit failed, stop leftovers with `ao testing cleanup`: "+clip(cell(cleanErr.Error()), 400))
	}

	if skipBase != "" {
		limits = append(limits, "base not run: "+clip(cell(skipBase), 300))
	}
	claims := claimsChange(runDir)
	v := Verdict{Checks: checks, Summary: strings.TrimSpace(summary), SkipBase: skipBase}
	if blocked != "" {
		v.Overall, v.Reasons = CheckBlocked, []string{blocked}
	} else {
		v.Overall, v.Reasons = derive(checks)
	}
	md := renderVerdict(run, v, claims, videos, runDir, s.clock(), limits)
	if err := s.store.WriteFile(run.ID, "verdict.md", []byte(md)); err != nil {
		return Verdict{}, err
	}
	if err := publishArtifacts(sessionartifacts.Dir(s.store.dataDir(), domain.SessionID(run.SessionID)), runDir, md, renderIndex(run, v, videos)); err != nil {
		return Verdict{}, fmt.Errorf("copy evidence to the session artifact dir: %w", err)
	}
	if s.mustAsk(run, v) { // compose the comment and stop: nothing is published or posted until the user approves it
		if err := s.store.put(run.ID, verdictFile, v); err != nil {
			return Verdict{}, fmt.Errorf("save the verdict for approval: %w", err)
		}
		if _, _, err := s.composeComment(run, v); err != nil {
			return Verdict{}, fmt.Errorf("write comment.md: %w", err)
		}
		v.CommentState, v.EvidenceState = CommentAwaiting, EvidenceOff
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.State, cur.CommentState, cur.EvidenceState = RunAwaitingApproval, v.CommentState, v.EvidenceState
			for i := range cur.Legs {
				if cleanErr == nil {
					cur.Legs[i].State = "cleaned"
				}
			}
		}); err != nil {
			return v, err
		}
	} else {
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.State = RunSubmitted
			for i := range cur.Legs {
				if cleanErr == nil {
					cur.Legs[i].State = "cleaned"
				}
			}
		}); err != nil {
			return v, err
		}
		var ev evidence
		v.CommentURL, v.CommentState, ev = s.postComment(ctx, run, v, videos) // never fails the submit
		v.EvidenceState, v.EvidenceCommit = ev.state, ev.commit
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.CommentURL, cur.CommentState, cur.EvidenceState, cur.EvidenceCommit = v.CommentURL, v.CommentState, v.EvidenceState, v.EvidenceCommit
			cur.VideoState, cur.VideoURLs = ev.videoState, ev.videoURLs()
		}); err != nil {
			slog.Warn("litmus: could not save the comment state", "run", run.ID, "error", err)
		}
	}
	if s.report != nil { // the tester's last act: the session closes itself without `ao report --done`
		note := fmt.Sprintf("Litmus verdict %s for %s#%d: see verdict.md in the session artifacts.", v.Overall, run.Repo, run.PRNumber)
		if err := s.report(ctx, run.SessionID, note); err != nil {
			slog.Warn("litmus: done report failed", "run", run.ID, "session", run.SessionID, "error", err)
		}
	}
	if s.kill != nil { // a finished tester would otherwise sit idle until someone noticed
		time.AfterFunc(endGrace, func() {
			kctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.kill(kctx, run.SessionID); err != nil {
				slog.Warn("litmus: could not stop the tester after its run closed", "run", run.ID, "session", run.SessionID, "error", err)
			}
		})
	}
	s.autoPrune(ctx, run) // the retention pass every finished run ends with; never fails the submit
	return v, nil
}

func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}

// relPath is p relative to the run dir, which is also where it sits in the session artifact dir.
func relPath(runDir, p string) string {
	if rel, err := filepath.Rel(runDir, p); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(p)
}

// fileExists reports whether p is a regular file: the verdict links only to evidence that is there.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// untestedLimit is the standing last line of every Limits list.
const untestedLimit = "Only the checks above were tried; the rest of the PR's changes are untested."

// compareCells is a check's head and base cells in the checks table. beside is true for a base-leg check
// that is shown beside its head twin and so gets no row of its own.
func compareCells(checks []Check, c Check) (head, base string, beside bool) {
	if c.onLeg() == "base" {
		if slices.ContainsFunc(checks, func(h Check) bool { return h.onLeg() == "head" && strings.EqualFold(h.Name, c.Name) }) {
			return "", "", true
		}
		return "-", string(c.State), false
	}
	switch base = baseResult(checks, c); {
	case base == string(CheckSkipped):
		base = "not compared on base"
	case base != "":
	case c.State == CheckSkipped:
		base = "-"
	default:
		base = "not compared on base"
	}
	return string(c.State), base, false
}

// checkLimit is the Limits line a check earns, or "": the tester could not reach it, or the base never showed it.
func checkLimit(checks []Check, c Check) string {
	switch {
	case c.State == CheckSkipped:
		return fmt.Sprintf("%s %s (%s leg): could not reach. %s", c.ID, c.Name, c.onLeg(), cell(c.Note))
	case preExisting(checks, c):
		return fmt.Sprintf("%s %s: pre-existing, same on base: %s", c.ID, c.Name, clip(cell(c.Note), 120))
	case c.onLeg() == "head" && baseResult(checks, c) == "":
		return fmt.Sprintf("%s %s: not compared on base, so whether the behavior changed cannot be told.", c.ID, c.Name)
	}
	return ""
}

func renderVerdict(run Run, v Verdict, claims bool, videos []legVideo, runDir string, now time.Time, limits []string) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("# Verdict: %s\n\n%s#%d, run %s, base %s, head %s\n", v.Overall, run.Repo, run.PRNumber, run.ID, run.BaseSHA, run.HeadSHA)
	p("Duration %s of a %s budget. Tokens: not measured by Litmus, see the session's usage. Video: %s.\n", now.Sub(run.CreatedAt).Round(time.Second), RunBudget, videoLine(videos))
	if v.Overall == CheckBlocked {
		p("## Blocked\n\n%s\n", strings.Join(v.Reasons, "\n"))
		if run.Error != "" {
			p("Daemon: %s\n", run.Error)
		}
	}
	if v.Summary != "" {
		p("## Summary\n\n%s\n", v.Summary)
	}
	p("## Checks\n\n| check | expect | head result | base result | receipts |\n|---|---|---|---|---|")
	for _, c := range v.Checks {
		head, base, beside := compareCells(v.Checks, c)
		if beside {
			continue // shown beside its head check
		}
		var links []string
		for _, r := range c.Receipts {
			switch {
			case r.OutputPath != "" && fileExists(r.OutputPath):
				links = append(links, fmt.Sprintf("[%d](%s)", r.Seq, relPath(runDir, r.OutputPath)))
			case r.AfterPath == "":
			case fileExists(r.AfterPath):
				links = append(links, fmt.Sprintf("[%d](%s)", r.Seq, relPath(runDir, r.AfterPath)))
			default:
				limits = append(limits, fmt.Sprintf("%s receipt %d: its after-shot file is missing, so it is not linked.", c.ID, r.Seq))
			}
		}
		p("| %s %s | %s | %s | %s | %s |", c.ID, cell(c.Name), cell(c.Expect), head, base, strings.Join(links, " "))
	}
	p("\n## Notes\n")
	for _, c := range v.Checks {
		p("### %s %s (%s leg): %s\n\nExpected: %s\n\nNote: %s\n", c.ID, c.Name, c.onLeg(), c.State, c.Expect, c.Note)
		switch {
		case c.Diff == "":
		case fileExists(c.Diff):
			p("Base vs head pixel diff: [%s](%s)\n", filepath.Base(c.Diff), relPath(runDir, c.Diff))
		default:
			limits = append(limits, fmt.Sprintf("%s pixel diff: the image file is missing, so it is not linked.", c.ID))
		}
	}
	acts := readActions(runDir)
	p("## Logs\n")
	for _, l := range run.Legs {
		tail := tailFile(filepath.Join(runDir, l.Name, "logs", "dev.log"), 30)
		p("### %s\n\nDaemon log tail:\n\n~~~~\n%s\n~~~~\n\nConsole errors:\n\n~~~~\n%s\n~~~~\n", l.Name, strings.ReplaceAll(tail, "~~~", "---"), strings.ReplaceAll(consoleErrors(acts, l.Name), "~~~", "---"))
	}
	p("## Limits\n")
	if !slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "running" }) {
		p("- The base leg was not launched, so nothing was observed on it.")
	}
	for _, c := range v.Checks {
		if l := checkLimit(v.Checks, c); l != "" {
			p("- %s", l)
		}
	}
	p("- %s", headlineRule)
	p("- The PR title %s a behavior change. That is informational and does not change the result.", map[bool]string{true: "claims", false: "does not claim"}[claims])
	if slices.ContainsFunc(acts, func(a actionRec) bool { return a.Unassigned }) {
		p("- Some state-changing actions ran while no check was open; they are not evidence for any check.")
	}
	for _, l := range limits {
		p("- %s", l)
	}
	p("- %s", untestedLimit)
	return b.String()
}

func renderIndex(run Run, v Verdict, videos []legVideo) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("# Litmus: %s#%d, verdict %s\n\nFull report: [verdict.md](verdict.md).\n", run.Repo, run.PRNumber, v.Overall)
	var links []string
	for _, vid := range videos {
		for _, f := range vid.files() {
			links = append(links, fmt.Sprintf("[%s](%s)", f, f))
		}
	}
	if len(links) > 0 {
		p("Video: %s.\n", strings.Join(links, ", "))
	}
	for _, c := range v.Checks {
		p("## %s %s: %s\n", c.ID, c.Name, c.State)
		for _, r := range c.Receipts {
			if r.AfterPath != "" && fileExists(r.AfterPath) {
				p(`<img src="receipts/%s" alt="%s %s" width="480">`, filepath.Base(r.AfterPath), r.onLeg(), r.Action)
			}
		}
		if c.Diff != "" && fileExists(c.Diff) {
			p(`<img src="receipts/%s" alt="pixel diff" width="480">`, filepath.Base(c.Diff))
		}
		p("")
	}
	return b.String()
}

// publishArtifacts copies the receipts and each leg's videos into the session's artifact dir and writes verdict.md and
// index.md beside them; index.md with relative <img> links is how AO shows content inline in the session.
func publishArtifacts(dir, runDir, verdictMD, indexMD string) error {
	if dir == "" {
		return errors.New("no artifact dir for this session")
	}
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o750); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(runDir, "receipts", "*"))
	copies := map[string]string{} // source -> destination
	for _, f := range files {
		copies[f] = filepath.Join(dir, relPath(runDir, f))
	}
	for _, leg := range []string{"head", "base"} { // each leg's recording and cut land beside verdict.md
		copies[recordingPath(runDir, leg)], copies[cutPath(runDir, leg)] = filepath.Join(dir, leg+".mp4"), filepath.Join(dir, leg+"-cut.mp4")
	}
	for src, dst := range copies {
		if fileExists(src) {
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	if err := writeAtomic(filepath.Join(dir, "verdict.md"), []byte(verdictMD)); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "index.md"), []byte(indexMD))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// readActions loads actions.jsonl up to its first unreadable line.
func readActions(runDir string) (recs []actionRec) {
	f, err := os.Open(filepath.Join(runDir, "actions.jsonl"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	for dec := json.NewDecoder(f); dec.More(); {
		var r actionRec
		if dec.Decode(&r) != nil {
			break
		}
		recs = append(recs, r)
	}
	return recs
}

// consoleErrors collects the distinct console lines mentioning an error that a leg's receipts captured.
func consoleErrors(acts []actionRec, leg string) string {
	var out []string
	for _, a := range acts {
		for _, l := range strings.Split(a.ConsoleTail, "\n") {
			if l = strings.TrimSpace(l); a.Leg == leg && strings.Contains(strings.ToLower(l), "error") && !slices.Contains(out, l) && len(out) < 10 {
				out = append(out, clip(l, 300))
			}
		}
	}
	return strings.Join(out, "\n")
}

// tailFile returns the last n lines of a file, reading only its final 64 KiB.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no log)"
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		_, _ = f.Seek(-(64 << 10), io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	lines := strings.Split(lastLines(data, n), "\n")
	for i, l := range lines {
		lines[i] = clip(l, 240)
	}
	return strings.Join(lines, "\n")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
