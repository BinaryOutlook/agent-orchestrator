package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

// testingCLI is the shared plumbing for `ao testing` verbs: each verb is one
// HTTP call to /api/v1/testing/... attributed to the AO_SESSION_ID worker.
type testingCLI struct {
	c      *commandContext
	asJSON bool
	leg    string
}

func verb(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: run}
}

func newTestingCommand(ctx *commandContext) *cobra.Command {
	t := &testingCLI{c: ctx}
	cmd := verb("testing", "Test a pull request: pin it, build base and head, drive the app, submit a verdict", noArgs, nil)
	cmd.PersistentFlags().BoolVar(&t.asJSON, "json", false, "print the structured response as JSON")
	cmd.PersistentFlags().StringVar(&t.leg, "leg", "", "leg a driving verb acts on (base or head); needed when both are launched")

	cmd.AddCommand(t.startCommand())
	cmd.AddCommand(verb("intake <pr-url>", "Start a testing run for a GitHub pull request (25 minute budget)", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCall(t, cmd, "testing/intake", map[string]any{"prUrl": a[0]}, runLine)
	}))
	cmd.AddCommand(verb("prepare", "Prepare the base and head clones and builds", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCall(t, cmd, "testing/prepare", map[string]any{}, runLine)
	}))
	cmd.AddCommand(verb("launch <base|head>", "Launch a private copy of the base or head build", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCall(t, cmd, "testing/launch", map[string]any{"leg": a[0]}, runLine)
	}))
	cmd.AddCommand(verb("status", "Show the run, its deadline and its checks", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCall(t, cmd, "testing/status", nil, func(out litmus.RunStatus) string {
			lines := make([]string, 0, 1+len(out.Run.Legs)+len(out.Checks))
			lines = append(lines, runLine(out.Run))
			for _, l := range out.Run.Legs {
				lines = append(lines, legLine(l))
			}
			for _, c := range out.Checks {
				lines = append(lines, fmt.Sprintf("%s (%d receipts)", checkLine(c), len(c.Receipts)))
			}
			return strings.Join(lines, "\n")
		})
	}))
	cmd.AddCommand(t.checkCommand(), t.compareCommand(), t.submitCommand())
	cmd.AddCommand(verb("cleanup", "Stop the run's targets and remove its private state", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCall(t, cmd, "testing/cleanup", map[string]any{}, runLine)
	}))
	cmd.AddCommand(t.recipeCommand(), t.waitCommand(), t.recordCommand(), t.testCommand())
	cmd.AddCommand(t.pruneCommand())

	for _, d := range []struct {
		use, short string
		args       cobra.PositionalArgs
	}{
		{"snapshot", "Read the UI as text with element refs", noArgs},
		{"click <ref>", "Click an element by ref", exactArgs(1)},
		{"fill <ref> <text>", "Replace an input's text", exactArgs(2)},
		{"type <ref> <text>", "Type text into an element", exactArgs(2)},
		{"press <key>", "Press a key", exactArgs(1)},
		{"screenshot [path]", "Save a screenshot", atMostOneArg},
		{"console", "Print console output", noArgs},
		{"errors", "Print page errors", noArgs},
		{"network", "Print network requests", noArgs},
		{"tab [list]", "List the target's tabs", atMostOneArg},
	} {
		action := strings.Fields(d.use)[0]
		cmd.AddCommand(verb(d.use, d.short, d.args, func(cmd *cobra.Command, a []string) error { return t.drive(cmd, action, a) }))
	}
	return cmd
}

// startCommand spawns the worker that tests a PR. The worker, not this command, makes the run.
func (t *testingCLI) startCommand() *cobra.Command {
	var pr, projectID, harness string
	var noComment, noEvidence bool
	cmd := verb("start --pr <url>", "Spawn a worker that tests a pull request (claude-code only)", noArgs, func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(pr) == "" {
			return usageError{errors.New("usage: --pr is required")}
		}
		project, err := t.c.resolveSpawnProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}
		agent, err := resolveSpawnHarness(harness, "worker", project)
		if err != nil && strings.TrimSpace(harness) == "" {
			agent, err = litmus.Harness, nil // nothing configured: testing has one harness
		}
		if err != nil {
			return err
		}
		if agent != litmus.Harness {
			return usageError{fmt.Errorf("testing workers run on %s only, not %q; pass --harness %s", litmus.Harness, agent, litmus.Harness)}
		}
		var out litmus.Started
		body := map[string]any{"projectId": project.ID, "harness": agent, "prUrl": pr}
		if noComment {
			body["noComment"] = true
		}
		if noEvidence {
			body["noEvidence"] = true
		}
		if err := t.c.postJSON(cmd.Context(), "testing/start", body, &out); err != nil {
			return err
		}
		return t.show(cmd, out, fmt.Sprintf("spawned session %s %q", out.SessionID, out.Name))
	})
	f := cmd.Flags()
	f.StringVar(&pr, "pr", "", "GitHub pull request URL (or owner/repo#N) to test")
	f.StringVar(&projectID, "project", "", "Project id the worker runs in (default: AO_PROJECT_ID or the current registered repo)")
	f.StringVar(&harness, "harness", "", "Agent harness; only claude-code is supported (default: the project's worker agent)")
	f.BoolVar(&noComment, "no-comment", false, "Do not post the verdict on the pull request (it is still written to comment.md in the run dir)")
	f.BoolVar(&noEvidence, "no-evidence", false, "Do not publish screenshots and GIFs to the repo's litmus/evidence branch for the comment")
	return cmd
}

func (t *testingCLI) checkCommand() *cobra.Command {
	check := verb("check", "Plan and bracket the checks a verdict is built from", noArgs, nil)
	var expect, result, note string
	add := verb("add <name>", "Add a check with the observable you expect", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if strings.TrimSpace(expect) == "" {
			return usageError{errors.New("usage: --expect is required")}
		}
		if t.leg != "" && t.leg != "base" && t.leg != "head" {
			return usageError{errors.New("usage: --leg must be base or head")}
		}
		body := map[string]any{"name": a[0], "expect": expect}
		if t.leg != "" {
			body["leg"] = t.leg
		}
		return testingCall(t, cmd, "testing/checks", body, checkLine)
	})
	add.Flags().StringVar(&expect, "expect", "", "Observable that proves the check passes (add it with --leg base to record what the base shows)")
	list := verb("list", "List the checks, their legs and receipts", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCall(t, cmd, "testing/status", nil, func(out litmus.RunStatus) string {
			lines := make([]string, 0, len(out.Checks))
			for _, c := range out.Checks {
				lines = append(lines, fmt.Sprintf("%s (%d receipts): %s", checkLine(c), len(c.Receipts), c.Expect))
			}
			return strings.Join(lines, "\n")
		})
	})
	start := verb("start <id>", "Start a check; actions record receipts against it", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/start", map[string]any{}, checkLine)
	})
	end := verb("end <id>", "End a check and record its outcome", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if result != "pass" && result != "fail" && result != "partial" && result != "skipped" {
			return usageError{errors.New("usage: --result must be pass, fail, partial or skipped")}
		}
		return testingCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/end", map[string]any{"result": result, "note": note}, checkLine)
	})
	end.Flags().StringVar(&result, "result", "", "Outcome: pass, fail, partial or skipped (could not reach it; needs no receipt)")
	end.Flags().StringVar(&note, "note", "", "Why, in one line")
	check.AddCommand(add, list, start, end)
	return check
}

// compareCommand pixel-diffs a check's last base after-shot against the head page; it reruns nothing.
func (t *testingCLI) compareCommand() *cobra.Command {
	return verb("compare <checkID>", "Diff a check's last base and head after-shots and keep the image on the check", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/compare", map[string]any{}, prettyJSON)
	})
}

func (t *testingCLI) submitCommand() *cobra.Command {
	var verdictFile, reason string
	var blocked, skipBase bool
	cmd := verb("submit", "Submit the verdict; every check needs an outcome and a receipt (--blocked --reason closes a run you cannot finish)", noArgs, func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		switch {
		case blocked && skipBase:
			return usageError{errors.New("usage: --blocked and --skip-base cannot be combined")}
		case blocked && strings.TrimSpace(reason) == "":
			return usageError{errors.New("usage: --blocked requires a non-empty --reason")}
		case skipBase && strings.TrimSpace(reason) == "":
			return usageError{errors.New("usage: --skip-base requires a non-empty --reason")}
		case blocked && verdictFile != "":
			return usageError{errors.New("usage: --blocked takes --reason, not --verdict-file")}
		case !blocked && !skipBase && reason != "":
			return usageError{errors.New("usage: --reason requires --blocked or --skip-base")}
		case blocked:
			body["blocked"], body["reason"] = true, strings.TrimSpace(reason)
		default:
			var summary string
			if verdictFile != "" {
				data, err := os.ReadFile(verdictFile)
				if err != nil {
					return err
				}
				summary = string(data)
			}
			if summary != "" {
				body["summary"] = summary
			}
			if skipBase {
				body["skipBase"] = strings.TrimSpace(reason)
			}
		}
		return testingCall(t, cmd, "testing/submit", body, func(v litmus.Verdict) string {
			return strings.Join(append(append(append([]string{"verdict: " + string(v.Overall)}, v.Reasons...), commentLine(v.CommentState, v.CommentURL)...), evidenceLine(v.EvidenceState, v.EvidenceCommit)...), "\n")
		})
	})
	cmd.Flags().StringVar(&verdictFile, "verdict-file", "", "File whose text is kept as the verdict summary")
	cmd.Flags().BoolVar(&blocked, "blocked", false, "Close the run as blocked: no receipts needed, the reason becomes the verdict")
	cmd.Flags().BoolVar(&skipBase, "skip-base", false, "Submit without a base run (honoured only if the base failed to launch or under 4 minutes remain); needs --reason")
	cmd.Flags().StringVar(&reason, "reason", "", "What failed (with --blocked) or why the base was not run (with --skip-base)")
	return cmd
}

func (t *testingCLI) recipeCommand() *cobra.Command {
	recipe := verb("recipe", "Work with per-repo recipes", noArgs, nil)
	var sha string
	validate := verb("validate <path>", "Validate a recipe file; with --sha also run it against that commit", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		abs, err := filepath.Abs(a[0]) // the daemon's working directory is not the worker's
		if err != nil {
			return err
		}
		ref, err := litmus.RecipeRef(abs, sha)
		if err != nil {
			return usageError{err}
		}
		return testingCall(t, cmd, "testing/recipe/validate", map[string]any{"path": ref}, func(r litmus.Recipe) string {
			return strings.Join(append([]string{"recipe ok: " + r.Kind}, r.Timings...), "\n")
		})
	})
	validate.Flags().StringVar(&sha, "sha", "", "Prepare, launch, seed and clean up a head leg at this commit and print the timings")
	recipe.AddCommand(validate)
	return recipe
}

func (t *testingCLI) waitCommand() *cobra.Command {
	var text string
	cmd := verb("wait [selector|ms]", "Wait for a selector, a delay in ms, or --text", atMostOneArg, func(cmd *cobra.Command, a []string) error {
		switch {
		case text != "" && len(a) == 0:
			a = []string{"--text", text}
		case text != "" || len(a) != 1:
			return usageError{errors.New("usage: wait takes one selector or ms, or --text")}
		}
		return t.drive(cmd, "wait", a)
	})
	cmd.Flags().StringVar(&text, "text", "", "Wait until this text appears")
	return cmd
}

// testCommand runs a package's own tests in a leg's checkout. The daemon runs them, so it needs no launched leg.
func (t *testingCLI) testCommand() *cobra.Command {
	var dir, withTestsFrom string
	cmd := verb("test --leg head|base --dir <subdir> -- <command...>", "Run go test, go vet, npm test or vitest/jest in a leg's checkout (3 minute cap)", func(cmd *cobra.Command, a []string) error {
		if err := cobra.MinimumNArgs(1)(cmd, a); err != nil {
			return usageError{errors.New("usage: ao testing test --leg head|base --dir <subdir> -- <command...>")}
		}
		return nil
	}, func(cmd *cobra.Command, a []string) error {
		if t.leg != "base" && t.leg != "head" {
			return usageError{errors.New("usage: --leg must be base or head")}
		}
		sessionID, err := testingSessionID()
		if err != nil {
			return err
		}
		var out litmus.TestResult
		body := map[string]any{"sessionId": sessionID, "leg": t.leg, "dir": dir, "args": a}
		if withTestsFrom != "" {
			body["withTestsFrom"] = withTestsFrom
		}
		if err := t.c.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/testing/test", body, &out, nil, testCallTimeout); err != nil {
			return err
		}
		return t.show(cmd, out, testLine(out))
	})
	cmd.Flags().StringVar(&dir, "dir", "", "Folder inside the checkout to run in, like backend")
	cmd.Flags().StringVar(&withTestsFrom, "with-tests-from", "", "head: with --leg base, run the PR's added or changed test files against the base code (in a scratch copy)")
	return cmd
}

// testCallTimeout outlasts the daemon's 3 minute cap on one test run.
const testCallTimeout = 4 * time.Minute

func testLine(r litmus.TestResult) string {
	head := fmt.Sprintf("%s: exit %d in %.1fs", strings.Join(r.Args, " "), r.Exit, r.Seconds)
	if len(r.CopiedFiles) > 0 {
		head += "\nPR test files copied from " + r.WithTestsFrom + ": " + strings.Join(r.CopiedFiles, ", ")
	}
	if r.TimedOut {
		head += " (timed out and killed)"
	}
	if r.Summary != "" {
		head += "\n" + r.Summary
	}
	return head + "\n" + r.Output + "\nfull output: " + r.Receipt
}

func (t *testingCLI) recordCommand() *cobra.Command {
	record := verb("record", "Record the target's window to a video", noArgs, nil)
	record.AddCommand(verb("start <path>", "Start recording to path", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return t.drive(cmd, "record", []string{"start", a[0]})
	}), verb("stop", "Stop recording", noArgs, func(cmd *cobra.Command, _ []string) error {
		return t.drive(cmd, "record", []string{"stop"})
	}))
	return record
}

// testingCall sends one request for the current session (a POST of body, or a
// GET when body is nil), decodes the daemon's answer as T and prints it with text.
func testingCall[T any](t *testingCLI, cmd *cobra.Command, path string, body map[string]any, text func(T) string) error {
	sessionID, err := testingSessionID()
	if err != nil {
		return err
	}
	var out T
	if body == nil {
		err = t.c.getJSON(cmd.Context(), path+"?sessionId="+url.QueryEscape(sessionID), &out)
	} else {
		body["sessionId"] = sessionID
		err = t.c.postJSON(cmd.Context(), path, body, &out)
	}
	if err != nil {
		return err
	}
	return t.show(cmd, out, text(out))
}

// drive sends one engine action; the engine's result is printed as JSON.
func (t *testingCLI) drive(cmd *cobra.Command, action string, args []string) error {
	if t.leg != "" {
		args = append(args[:len(args):len(args)], "--leg", t.leg)
	}
	return testingCall(t, cmd, "testing/drive", map[string]any{"action": action, "args": args}, prettyJSON)
}

func prettyJSON(out map[string]any) string {
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

func (t *testingCLI) show(cmd *cobra.Command, v any, text string) error {
	if t.asJSON {
		return writeJSON(cmd.OutOrStdout(), v)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), text)
	return err
}

func testingSessionID() (string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", usageError{errors.New("usage: AO_SESSION_ID is required")}
	}
	return sessionID, nil
}

func runLine(r litmus.Run) string {
	line := fmt.Sprintf("run %s %s %s#%d deadline %s", r.ID, r.State, r.Repo, r.PRNumber, r.Deadline.Format("15:04:05"))
	if r.Error != "" {
		line += "\n" + r.Error
	}
	return strings.Join(append(append([]string{line}, commentLine(r.CommentState, r.CommentURL)...), evidenceLine(r.EvidenceState, r.EvidenceCommit)...), "\n")
}

// commentLine says what became of the PR comment: its URL, or the state when there is none (off, failed:<reason>).
// A run that has not submitted has neither and gets no line.
func commentLine(state, link string) []string {
	switch {
	case link != "":
		return []string{"comment " + state + ": " + link}
	case state != "":
		return []string{"comment " + state}
	}
	return nil
}

// evidenceLine says what became of the comment's images: published at a commit, off, or why not.
func evidenceLine(state, commit string) []string {
	switch {
	case commit != "":
		return []string{"evidence " + state + ": " + commit[:min(len(commit), 7)]}
	case state != "":
		return []string{"evidence " + state}
	}
	return nil
}

func legLine(l litmus.Leg) string {
	line := fmt.Sprintf("  %s %s", l.Name, l.State)
	if l.State == "running" {
		line += fmt.Sprintf(" daemon :%d debug :%d", l.DaemonPort, l.DebugPort)
	}
	return line
}

// pruneCommand asks the daemon to apply the disk retention rules; it needs no session.
func (t *testingCLI) pruneCommand() *cobra.Command {
	var keepRuns, keepCheckouts int
	var dryRun bool
	cmd := verb("prune", "Delete old checkouts and run dirs; keeps those of unfinished runs", noArgs, func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		if cmd.Flags().Changed("keep-runs") {
			body["keepRuns"] = keepRuns
		}
		if cmd.Flags().Changed("keep-checkouts") {
			body["keepCheckouts"] = keepCheckouts
		}
		if dryRun {
			body["dryRun"] = true
		}
		var out litmus.PruneReport
		if err := t.c.postJSON(cmd.Context(), "testing/prune", body, &out); err != nil {
			return err
		}
		return t.show(cmd, out, strings.Join(out.Lines, "\n"))
	})
	f := cmd.Flags()
	f.IntVar(&keepRuns, "keep-runs", litmus.DefaultKeepRuns, "Finished run dirs to keep besides unfinished runs and the current one; 0 keeps only the current and unfinished runs")
	f.IntVar(&keepCheckouts, "keep-checkouts", litmus.DefaultKeepCheckouts, "Most recent finished runs whose checkouts are kept besides those of unfinished runs")
	f.BoolVar(&dryRun, "dry-run", false, "Report what would be deleted and delete nothing")
	return cmd
}

func checkLine(c litmus.Check) string {
	line := fmt.Sprintf("%s %s %s", c.ID, c.State, c.Name)
	if c.Leg != "" {
		line += " [" + c.Leg + "]"
	}
	return line
}
