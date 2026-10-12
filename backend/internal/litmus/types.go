// Package litmus implements agentic PR testing: a run pins a PR, a worker
// records checks against privately launched builds, and submit turns the
// receipts into a verdict. These JSON shapes are the on-disk and wire contract.
package litmus

import (
	"cmp"
	"time"
)

// RunBudget is how long a run may live, measured from intake.
const RunBudget = 25 * time.Minute

// A tester that has not run intake is reaped when its session has been exited or idle for IdleBudget, or SpawnBudget after it was spawned.
const (
	IdleBudget  = 2 * time.Minute
	SpawnBudget = 5 * time.Minute
	// ReasonNoIntake is the reason on a run expired before its tester ever ran intake.
	ReasonNoIntake = "the tester never ran intake"
)

// RunState is the lifecycle position of a run.
type RunState string

// Run states. Submitted, expired and cleaned are closed.
const (
	RunIntake    RunState = "intake"
	RunPrepared  RunState = "prepared"
	RunRunning   RunState = "running"
	RunSubmitted RunState = "submitted"
	// RunAwaitingApproval is a submitted run of a project that asks before posting: the verdict and comment.md are
	// written, nothing is published, and the run waits for a post or a discard. It has no deadline.
	RunAwaitingApproval RunState = "awaiting-approval"
	RunExpired          RunState = "expired"
	RunCleaned          RunState = "cleaned"
)

// Closed reports whether the run accepts no further worker verbs.
func (s RunState) Closed() bool {
	return s == RunSubmitted || s == RunAwaitingApproval || s == RunExpired || s == RunCleaned
}

// Run is one PR test attempt, owned by one AO session.
type Run struct {
	ID            string     `json:"id"`
	SessionID     string     `json:"sessionId"`
	ProjectID     string     `json:"projectId,omitempty"`     // the AO project the tester was started in; empty on runs older than the Litmus panel
	Repo          string     `json:"repo"`                    // as gh resolved the PR (follows renames), lowercase owner/repo
	RequestedRepo string     `json:"requestedRepo,omitempty"` // owner/repo from the URL the user gave
	PRNumber      int        `json:"prNumber"`
	BaseSHA       string     `json:"baseSha,omitempty"`
	HeadSHA       string     `json:"headSha,omitempty"`
	Issues        []IssueRef `json:"issues,omitempty"`  // the issues the PR closes (at most three), so the comment can say what the PR is for
	Areas         []Area     `json:"areas,omitempty"`   // the parts of the codebase the PR changes, in words, biggest first
	BaseRef       string     `json:"baseRef,omitempty"` // the PR's base branch name, as the comment calls the base build; empty on older runs (main)
	State         RunState   `json:"state"`
	CreatedAt     time.Time  `json:"createdAt"`
	Deadline      time.Time  `json:"deadline"`
	Legs          []Leg      `json:"legs,omitempty"`
	Error         string     `json:"error,omitempty"`        // why the last intake failed (a run with no pinned SHAs may retry), or "blocked at prepare: ..."
	Comment       string     `json:"comment,omitempty"`      // post | off: whether submit posts the verdict to the PR; set by intake, empty on older runs (no post)
	CommentURL    string     `json:"commentUrl,omitempty"`   // the PR comment submit posted or updated
	CommentState  string     `json:"commentState,omitempty"` // posted | updated | off | failed:<reason>
	// Evidence is whether submit publishes screenshots and GIFs for the comment: post | off; empty on older runs (off).
	Evidence       string `json:"evidence,omitempty"`
	EvidenceState  string `json:"evidenceState,omitempty"`  // published | off | failed:<reason>
	EvidenceCommit string `json:"evidenceCommit,omitempty"` // the litmus/evidence commit the comment's image links are pinned to
	RepoVisibility string `json:"repoVisibility,omitempty"` // public | private: how image links are written; asked of gh once
	// RepoID is the repo's numeric id, asked of gh once: the video upload endpoint wants it. --no-evidence also turns videos off.
	RepoID     int64             `json:"repoId,omitempty"`
	VideoState string            `json:"videoState,omitempty"` // attached | partial | off | failed:<reason>
	VideoURLs  map[string]string `json:"videoUrls,omitempty"`  // by leg (head | base): the GitHub link of the uploaded video
}

// IssueRef is an issue the PR says it closes.
type IssueRef struct {
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

// Area is a part of the codebase a PR changes ("Renderer components", "Tests") and how many of its files changed.
// Stems are the names of its first eight files without the extension, so the comment can name a lone file.
type Area struct {
	Name  string   `json:"name"`
	Files int      `json:"files"`
	Stems []string `json:"stems,omitempty"`
}

// Leg is one launched target: the PR's base or head, built and run privately.
type Leg struct {
	Name          string      `json:"name"` // base | head
	Worktree      string      `json:"worktree,omitempty"`
	BuildManifest string      `json:"buildManifest,omitempty"`
	PID           int         `json:"pid,omitempty"`
	PIDStart      time.Time   `json:"pidStart,omitzero"`
	DataDir       string      `json:"dataDir,omitempty"`
	DaemonPort    int         `json:"daemonPort,omitempty"`
	DebugPort     int         `json:"debugPort,omitempty"`
	RunFile       string      `json:"runFile,omitempty"`
	Engine        *EngineInfo `json:"engine,omitempty"`
	State         string      `json:"state"`
	// RecordStartedAt is when `record start` succeeded on this leg's engine, so action times map onto <leg>/<leg>.mp4. Zero: no recording.
	RecordStartedAt time.Time `json:"recordStartedAt,omitzero"`
	// RecordStoppedAt is when `record stop` was settled (or the file dropped); started without stopped means the recording never finished.
	RecordStoppedAt time.Time `json:"recordStoppedAt,omitzero"`
	RecordCursor    bool      `json:"recordCursor,omitempty"` // the recording shows the pointer overlay (`record start --cursor`); false after the fallback to a plain start
	RecordFPS       string    `json:"recordFps,omitempty"`    // the capture rate asked of `record start --fps`
}

// CheckState is a check's progress; pass, fail, partial and skipped are its outcomes.
type CheckState string

// Check states.
const (
	CheckPending CheckState = "pending"
	CheckRunning CheckState = "running"
	CheckPass    CheckState = "pass"
	CheckFail    CheckState = "fail"
	CheckPartial CheckState = "partial"
	// CheckSkipped is the tester's "could not reach": the check needs no receipt and stays out of the headline.
	CheckSkipped CheckState = "skipped"
	// CheckBlocked is only ever a verdict's overall: the tester could not run the checks.
	CheckBlocked CheckState = "blocked"
)

// Check is one planned observation and the evidence recorded for it.
type Check struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Expect   string     `json:"expect"`
	State    CheckState `json:"state"`
	Note     string     `json:"note,omitempty"`
	Leg      string     `json:"leg,omitempty"`  // base | head; empty means head
	Diff     string     `json:"diff,omitempty"` // pixel-diff image of this check's base and head after-shots (`ao testing compare`)
	Receipts []Receipt  `json:"receipts,omitempty"`
}

// onLeg is the leg the check observes; checks declared without one observe the head.
func (c Check) onLeg() string { return cmp.Or(c.Leg, "head") }

// Receipt is the evidence for one state-changing action.
type Receipt struct {
	Seq         int       `json:"seq"`
	Leg         string    `json:"leg,omitempty"` // the leg that was driven; empty means head
	Action      string    `json:"action"`
	Args        []string  `json:"args,omitempty"`
	BeforePath  string    `json:"beforePath,omitempty"`
	AfterPath   string    `json:"afterPath,omitempty"`
	ConsoleTail string    `json:"consoleTail,omitempty"`
	At          time.Time `json:"at"`
	// OutputPath is the full output of a test action (receipts/<n>-test.txt). It is not a shot: AfterPath stays empty.
	OutputPath string `json:"outputPath,omitempty"`
	// TargetRole and TargetName are what the tester's last snapshot called the element it acted on, for plain captions.
	TargetRole string `json:"targetRole,omitempty"`
	TargetName string `json:"targetName,omitempty"`
}

// baseName is what a reader calls the base build: the PR's base branch, main when the run did not record it.
func (r Run) baseName() string { return mdText(cmp.Or(r.BaseRef, "main"), 60) }

// sideName is how prose names a leg: "the PR build" for the head, the base branch for the base.
func (r Run) sideName(leg string) string {
	if leg == "base" {
		return r.baseName()
	}
	return "the PR build"
}

func (r Receipt) onLeg() string { return cmp.Or(r.Leg, "head") }

// Verdict is the submitted result: per-check outcomes with their receipts.
type Verdict struct {
	Overall  CheckState `json:"overall"` // pass | fail | partial | blocked
	Checks   []Check    `json:"checks"`
	Reasons  []string   `json:"reasons,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	SkipBase string     `json:"skipBase,omitempty"` // why the tester did not run the base (submit --skip-base --reason)
	// CommentURL and CommentState echo the run's: where submit put the verdict on the PR, and how that went.
	CommentURL   string `json:"commentUrl,omitempty"`
	CommentState string `json:"commentState,omitempty"`
	// EvidenceState and EvidenceCommit echo the run's: whether the comment's images were published, and where.
	EvidenceState  string `json:"evidenceState,omitempty"`
	EvidenceCommit string `json:"evidenceCommit,omitempty"`
}

// RunStatus is a run with its checks.
type RunStatus struct {
	Run    Run     `json:"run"`
	Checks []Check `json:"checks"`
}
