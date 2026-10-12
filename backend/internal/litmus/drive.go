package litmus

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Tests shorten these.
var (
	settleDelay = 400 * time.Millisecond // let the UI react before the after-shot
	settleLoad  = 3 * time.Second        // cap on `wait --load`, whose failure is ignored
	shotRetry   = time.Second            // pause before the one retry of an after-shot that timed out
)

// verb says how a worker action maps onto the engine. No open, navigate, eval, upload,
// download, cookies, storage, auth, plugin or pushstate verb exists here.
type verb struct {
	engine   []string // engine words ahead of the caller's arguments; default is the action name
	min, max int      // caller arguments, flags included
	refs     int      // leading arguments that are element refs
	receipt  bool     // changes state: settle, screenshot and record it
	usage    string
}

var verbs = map[string]verb{
	"snapshot":   {max: 6, usage: "snapshot [-i] [-c] [-d <n>] [-s <selector>]"},
	"click":      {min: 1, max: 1, refs: 1, receipt: true, usage: "click <ref>"},
	"fill":       {min: 2, max: 2, refs: 1, receipt: true, usage: "fill <ref> <text>"},
	"type":       {min: 2, max: 2, refs: 1, receipt: true, usage: "type <ref> <text>"},
	"press":      {min: 1, max: 1, receipt: true, usage: "press <key>"},
	"select":     {min: 2, max: 2, refs: 1, receipt: true, usage: "select <ref> <value>"},
	"check":      {min: 1, max: 1, refs: 1, receipt: true, usage: "check <ref>"},
	"uncheck":    {min: 1, max: 1, refs: 1, receipt: true, usage: "uncheck <ref>"},
	"drag":       {min: 2, max: 2, refs: 2, receipt: true, usage: "drag <ref> <ref>"},
	"wait":       {min: 1, max: 2, usage: "wait <selector|ms> | wait --text <text>"},
	"screenshot": {max: 1, usage: "screenshot [path]"},
	"console":    {usage: "console"},
	"errors":     {usage: "errors"},
	"network":    {engine: []string{"network", "requests"}, max: 2, usage: "network [--filter <pattern>]"},
	"record":     {min: 1, max: 2, usage: "record start <path> | record stop"},
	"tab":        {engine: []string{"tab", "list"}, max: 1, usage: "tab list"},
}

var refPattern = regexp.MustCompile(`^@?(e\d+)$`)

// splitLeg pulls "--leg <name>" or "--leg=<name>" out of args; the CLI appends it when --leg is given.
func splitLeg(args []string) (leg string, rest []string) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--leg" && i+1 < len(args):
			leg, i = args[i+1], i+1
		case strings.HasPrefix(a, "--leg="):
			leg = strings.TrimPrefix(a, "--leg=")
		default:
			rest = append(rest, a)
		}
	}
	return leg, rest
}

// pickLeg names the launched leg an action applies to; without --leg it must be unambiguous.
func pickLeg(run Run, name string) (Leg, error) {
	var live []Leg
	for _, l := range run.Legs {
		if l.State == "running" && l.Engine != nil {
			live = append(live, l)
		}
	}
	switch {
	case name != "":
		if i := slices.IndexFunc(live, func(l Leg) bool { return l.Name == name }); i >= 0 {
			return live[i], nil
		}
		return Leg{}, conflict("TESTING_LEG_NOT_LAUNCHED", "leg "+name+" is not launched; run `ao testing launch "+name+"`")
	case len(live) == 1:
		return live[0], nil
	case len(live) == 0:
		return Leg{}, conflict("TESTING_LEG_NOT_LAUNCHED", "no leg is launched; run `ao testing launch head`")
	}
	return Leg{}, invalid("TESTING_LEG_REQUIRED", "both legs are launched; name one with --leg base|head")
}

// inRun resolves an output path the worker named: relative paths land in the run dir, and nothing may leave it.
func inRun(runDir, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(runDir, p)
	}
	if rel, err := filepath.Rel(runDir, filepath.Clean(p)); err != nil || !filepath.IsLocal(rel) {
		return "", invalid("TESTING_PATH_INVALID", "output files must stay inside "+runDir)
	}
	return filepath.Clean(p), os.MkdirAll(filepath.Dir(p), 0o750)
}

// argv validates one action and returns the engine command and, for screenshot and record start, the file it writes.
func argv(action string, args []string, runDir, leg string) (cmd []string, file string, err error) {
	v := verbs[action]
	bad := func(msg string) ([]string, string, error) {
		return nil, "", invalid("TESTING_ARGS_INVALID", msg+"; usage: "+v.usage)
	}
	if len(args) < v.min || len(args) > v.max {
		return bad("wrong number of arguments")
	}
	for _, a := range args {
		l := strings.ToLower(a)
		if slices.ContainsFunc(forbiddenFlags, func(f string) bool { return l == f || strings.HasPrefix(l, f+"=") }) {
			return bad("flag " + a + " is managed by Litmus")
		}
	}
	args = slices.Clone(args)
	for i := 0; i < v.refs; i++ {
		m := refPattern.FindStringSubmatch(args[i])
		if m == nil {
			return bad(fmt.Sprintf("%q is not an element ref; take refs like @e12 from `snapshot`", args[i]))
		}
		args[i] = "@" + m[1]
	}
	switch action {
	case "tab":
		if len(args) == 1 && args[0] != "list" {
			return bad("only `tab list` is supported")
		}
		args = nil
	case "record":
		if start := args[0] == "start"; !start && args[0] != "stop" || start != (len(args) == 2) {
			return bad("unsupported record arguments")
		} else if start {
			if file, err = inRun(runDir, args[1]); err != nil {
				return nil, "", err
			}
			args = []string{"start", file}
		}
	case "screenshot":
		if file, err = inRun(runDir, cmp.Or(strings.Join(args, ""), filepath.Join("shots", fmt.Sprintf("%s-%d.png", leg, time.Now().UnixMilli())))); err != nil {
			return nil, "", err
		}
		args = []string{file}
	}
	return append(slices.Clone(cmdWords(action, v)), args...), file, nil
}

func cmdWords(action string, v verb) []string {
	if v.engine != nil {
		return v.engine
	}
	return []string{action}
}

// Drive runs one worker action on a launched leg. It is refused unless the run is
// running, inside its deadline and the named leg is launched.
func (s *Service) Drive(ctx context.Context, sessionID, action string, args []string) (map[string]any, error) {
	v, ok := verbs[action]
	if !ok {
		return nil, invalid("TESTING_ACTION_UNSUPPORTED", "unsupported testing action "+fmt.Sprintf("%q", action))
	}
	legName, args := splitLeg(args)
	s.mu.Lock()
	run, err := s.load(sessionID)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if run.State != RunRunning {
		return nil, conflict("TESTING_RUN_NOT_RUNNING", fmt.Sprintf("run %s is %s; launch a leg first", run.ID, run.State))
	}
	leg, err := pickLeg(run, legName)
	if err != nil {
		return nil, err
	}
	runDir := filepath.Join(s.store.root, run.ID)
	cmd, file, err := argv(action, args, runDir, leg.Name)
	if err != nil {
		return nil, err
	}

	e := s.engineFor(run.ID, leg)
	e.mu.Lock()
	defer e.mu.Unlock()
	if v.receipt && e.last == "" { // nothing before it yet: this leg's first state change gets a before-shot of its own
		if p := filepath.Join(runDir, "receipts", leg.Name+"-before.png"); s.shoot(ctx, e, p) == nil {
			e.last = p
		}
	}
	var target refInfo // what the last snapshot called the element about to be acted on, read before the page changes
	if v.refs > 0 {
		target = e.refs[cmd[len(cmdWords(action, v))]]
	}
	t0 := time.Now()
	out, err := e.Run(ctx, cmdTimeout, cmd...)
	if err != nil {
		return nil, apierr.Unavailable("TESTING_ENGINE_FAILED", err.Error())
	}
	if action == "snapshot" {
		e.refs = parseRefs(out)
	}
	res := map[string]any{"leg": leg.Name, "action": action, "output": out}
	if file != "" {
		res["path"] = file
	}
	if v.receipt {
		rec := s.receipt(ctx, run.ID, e, leg.Name, action, cmd[len(cmdWords(action, v)):], target, time.Since(t0))
		res["receipt"] = rec
		if rec.After != "" { // the tester reads this shot to judge anything visual
			res["after"] = relPath(runDir, rec.After)
		}
	}
	return res, nil
}

// shoot saves a screenshot and, on macOS, downsizes it with the built-in sips (no Go image dependency).
func (s *Service) shoot(ctx context.Context, e *Engine, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := e.Run(ctx, cmdTimeout, "screenshot", path); err != nil {
		return err
	}
	_, _ = s.runner.Run(ctx, "", nil, "sips", "-Z", "1320", path)
	return nil
}

// actionRec is one line of actions.jsonl.
type actionRec struct {
	Seq         int       `json:"seq"`
	Leg         string    `json:"leg"`
	Action      string    `json:"action"`
	Args        []string  `json:"args,omitempty"`
	Before      string    `json:"before"`
	After       string    `json:"after"`
	ConsoleTail string    `json:"consoleTail"`
	At          time.Time `json:"at"`
	MS          int64     `json:"ms"`
	Unassigned  bool      `json:"unassigned,omitempty"` // no check was open when it happened
	Warnings    []string  `json:"warnings,omitempty"`
	TargetRole  string    `json:"targetRole,omitempty"` // from the last snapshot: the element acted on, for plain captions
	TargetName  string    `json:"targetName,omitempty"`
	Exit        *int      `json:"exit,omitempty"` // test only: the command's exit code
	// WithTestsFrom and CopiedFiles are test only: the PR's test files copied from that leg onto the base checkout.
	WithTestsFrom string   `json:"withTestsFrom,omitempty"`
	CopiedFiles   []string `json:"copiedFiles,omitempty"`
}

// receipt settles the UI, takes the after-shot and the console tail, appends the record to
// actions.jsonl and attaches it to the open check. Its failures are warnings: the action already happened.
func (s *Service) receipt(ctx context.Context, runID string, e *Engine, leg, action string, args []string, target refInfo, took time.Duration) actionRec {
	s.mu.Lock()
	seq := s.nextSeq(runID)
	s.mu.Unlock()
	rec := actionRec{Seq: seq, Leg: leg, Action: action, Args: args, Before: e.last, At: s.clock(), MS: took.Milliseconds(), TargetRole: target.role, TargetName: target.name}
	warn := func(what string, err error) { rec.Warnings = append(rec.Warnings, what+": "+err.Error()) }

	select {
	case <-time.After(settleDelay):
	case <-ctx.Done():
	}
	_, _ = e.Run(ctx, settleLoad, "wait", "--load", "load")
	rec.After = filepath.Join(e.RunDir, "receipts", fmt.Sprintf("%d-after.png", seq))
	err := s.shoot(ctx, e, rec.After)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil { // a window the OS hid often paints on the next frame
		select {
		case <-time.After(shotRetry):
			err = s.shoot(ctx, e, rec.After)
		case <-ctx.Done():
		}
	}
	if err != nil {
		rec.After = ""
		warn("after-shot", err)
	} else {
		e.last = rec.After
	}
	if out, err := e.Run(ctx, cmdTimeout, "console"); err != nil {
		warn("console", err)
	} else {
		rec.ConsoleTail = lastLines([]byte(out), 20)
		rec.ConsoleTail = rec.ConsoleTail[:min(len(rec.ConsoleTail), 4000)]
	}

	s.attach(runID, &rec, Receipt{Seq: seq, Leg: leg, Action: action, Args: args, BeforePath: rec.Before, AfterPath: rec.After, ConsoleTail: rec.ConsoleTail, At: rec.At, TargetRole: target.role, TargetName: target.name})
	return rec
}

// attach adds r to the open check (rec is marked unassigned when none is open) and appends rec to
// actions.jsonl. Failures are warnings on rec: the action already happened.
func (s *Service) attach(runID string, rec *actionRec, r Receipt) {
	warn := func(what string, err error) { rec.Warnings = append(rec.Warnings, what+": "+err.Error()) }
	s.mu.Lock()
	defer s.mu.Unlock()
	checks, err := s.store.Checks(runID)
	if i := slices.IndexFunc(checks, func(c Check) bool { return c.State == CheckRunning }); err == nil && i >= 0 {
		checks[i].Receipts = append(checks[i].Receipts, r)
		err = s.store.SaveChecks(runID, checks)
	} else {
		rec.Unassigned = true
	}
	if err != nil {
		warn("check", err)
	}
	if path, err := s.store.path(runID, "actions.jsonl"); err != nil {
		warn("actions.jsonl", err)
	} else if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		warn("actions.jsonl", err)
	} else {
		if err := json.NewEncoder(f).Encode(*rec); err != nil {
			warn("actions.jsonl", err)
		}
		_ = f.Close()
	}
}

// nextSeq numbers a run's receipts from 1, continuing after a daemon restart. Callers hold s.mu.
func (s *Service) nextSeq(runID string) int {
	n, ok := s.seqs[runID]
	if path, err := s.store.path(runID, "actions.jsonl"); !ok && err == nil {
		data, _ := os.ReadFile(path)
		n = bytes.Count(data, []byte("\n"))
	}
	s.seqs[runID] = n + 1
	return n + 1
}

// engineFor returns the leg's engine, rebuilding it from run.json after a daemon restart.
func (s *Service) engineFor(runID string, leg Leg) *Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runID + "/" + leg.Name
	if s.engines[key] == nil {
		s.engines[key] = newEngine(*leg.Engine, s.engExec)
	}
	return s.engines[key]
}

// attachEngine gives a freshly launched leg its engine. Failing here undoes the launch.
func (s *Service) attachEngine(ctx context.Context, runID string, leg Leg) (Leg, error) {
	e, err := NewEngine(s.binary, filepath.Join(s.store.root, runID), leg.Name, leg.DebugPort, s.engExec)
	if err != nil {
		_ = cleanLeg(ctx, s.sys, s.store.legDir(runID, leg.Name), false)
		leg.State = "failed"
		return leg, err
	}
	leg.Engine = &e.EngineInfo
	s.mu.Lock()
	s.engines[runID+"/"+leg.Name] = e
	s.mu.Unlock()
	return leg, nil
}

// closeEngines closes every engine of a run, whether or not this daemon created it.
func (s *Service) closeEngines(runID string) {
	run, err := s.store.Get(runID)
	if err != nil {
		return
	}
	for _, l := range run.Legs {
		if l.Engine != nil {
			s.engineFor(runID, l).Close(s.sys)
		}
	}
	s.mu.Lock()
	for k := range s.engines {
		if strings.HasPrefix(k, runID+"/") {
			delete(s.engines, k)
		}
	}
	s.mu.Unlock()
}

// lastShot is the newest after-shot on leg among the receipts of every check called name.
func lastShot(checks []Check, name, leg string) (path string) {
	newest := 0
	for _, c := range checks {
		for _, r := range c.Receipts {
			if strings.EqualFold(c.Name, name) && r.onLeg() == leg && r.AfterPath != "" && r.Seq > newest {
				path, newest = r.AfterPath, r.Seq
			}
		}
	}
	return path
}

// Compare pairs a check's last after-shot on each leg and has the head engine pixel-diff the base shot against
// the head page (the engine can only diff a file against its live page). It reruns nothing; the diff image
// lands in receipts/ and its path on the check.
func (s *Service) Compare(ctx context.Context, sessionID, checkID string) (map[string]any, error) {
	checkID = strings.TrimSpace(checkID)
	if checkID == "" {
		return nil, invalid("TESTING_ARGS_INVALID", "wrong number of arguments; usage: compare <checkID>")
	}
	s.mu.Lock()
	run, err := s.load(sessionID)
	var checks []Check
	if err == nil {
		checks, err = s.store.Checks(run.ID)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(checks, func(c Check) bool { return c.ID == checkID })
	if i < 0 {
		return nil, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+fmt.Sprintf("%q", checkID))
	}
	head, base := lastShot(checks, checks[i].Name, "head"), lastShot(checks, checks[i].Name, "base")
	if head == "" || base == "" {
		return nil, conflict("TESTING_COMPARE_INCOMPLETE", fmt.Sprintf("check %s needs an after-shot on both legs to compare (head %t, base %t); act on it with --leg head and --leg base", checkID, head != "", base != ""))
	}
	leg, err := pickLeg(run, "head")
	if err != nil {
		return nil, err
	}
	out := filepath.Join(s.store.root, run.ID, "receipts", "diff-"+checkID+".png")
	_ = os.Remove(out) // an older image must not stand in for this one
	e := s.engineFor(run.ID, leg)
	e.mu.Lock()
	text, err := e.Run(ctx, cmdTimeout, "diff", "screenshot", "--baseline", base, "--output", out)
	e.mu.Unlock()
	if err != nil {
		return nil, apierr.Unavailable("TESTING_ENGINE_FAILED", err.Error())
	}
	res := map[string]any{"check": checkID, "base": base, "head": head, "output": text}
	if !fileExists(out) { // the engine may skip the image (nothing differs, or it could not diff); the check keeps no path to a file that is not there
		res["diff"], res["warning"] = "", "the engine wrote no diff image; the check keeps no diff"
		return res, nil
	}
	if _, err := s.updateCheck(sessionID, checkID, func(c *Check, _ []Check) error { c.Diff = out; return nil }); err != nil {
		return nil, err
	}
	res["diff"] = out
	return res, nil
}
