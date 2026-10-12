package litmus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// MatchRepo reports whether a repository URL or git remote names owner/repo on GitHub, ignoring case.
func MatchRepo(rawURL, owner, repo string) bool {
	id, err := domain.ParseRepositoryIdentity(rawURL)
	return err == nil && id.Provider == "github" && strings.EqualFold(id.Namespace, owner) && strings.EqualFold(id.Name, repo)
}

// RepoResolver returns the local checkout of a repo that clones are made from. names are
// owner/repo spellings of the same repository (as gh resolved it, as the user wrote it).
// A failure to find one carries the message the worker sees.
type RepoResolver func(ctx context.Context, names ...string) (string, error)

// localCheckout resolves names to a registered project's checkout, or a TESTING_REPO_UNRESOLVED error.
func (s *Service) localCheckout(ctx context.Context, names ...string) (string, error) {
	names = slices.Compact(slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "" }))
	var local string
	err := errors.New("no project registry is wired")
	if s.resolve != nil {
		if local, err = s.resolve(ctx, names...); err == nil && local == "" {
			err = fmt.Errorf("no registered AO project matches %s; add the checkout with `ao project add`", strings.Join(names, " or "))
		}
	}
	if err != nil {
		return "", apierr.NotFound("TESTING_REPO_UNRESOLVED", err.Error())
	}
	return local, nil
}

// Runner runs a command to completion and returns its combined output; a nil env
// inherits the daemon's. Tests swap it so no git or npm ever runs.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := process.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, lastLines(out, 20))
	}
	return out, err
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// baseEnv is the daemon's environment minus anything that could steer a target
// at the real AO (AO_*) or make Electron behave as plain node, with nodeDir first on PATH.
func baseEnv(nodeDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "AO_"), strings.HasPrefix(kv, "ELECTRON_RUN_AS_NODE="):
			continue
		case strings.HasPrefix(kv, "PATH=") && nodeDir != "":
			kv = "PATH=" + nodeDir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

func defaultNodeDirs(major string) []string {
	home, _ := os.UserHomeDir()
	return []string{"/usr/local/bin", "/opt/homebrew/bin", filepath.Join(home, ".nvm", "versions", "node", "v"+major+"*", "bin"), filepath.Join(home, ".hermes", "node", "bin")}
}

// probeNode finds a node binary of the pinned major version; no pin means the daemon's PATH decides.
func (s *Service) probeNode(ctx context.Context, major string) (path, version string, err error) {
	if major == "" {
		return "", "", nil
	}
	for _, pattern := range s.nodeDirs(major) {
		dirs, _ := filepath.Glob(pattern)
		for _, d := range dirs {
			bin := filepath.Join(d, "node")
			if _, err := os.Stat(bin); err != nil {
				continue
			}
			if out, err := s.runner.Run(ctx, "", nil, bin, "--version"); err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "v"+major+".") {
				return bin, strings.TrimSpace(string(out)), nil
			}
		}
	}
	return "", "", apierr.Unavailable("TESTING_NODE_MISSING", "no Node "+major+" found in /usr/local/bin, /opt/homebrew/bin, ~/.nvm or ~/.hermes; install it")
}

// Manifest is manifest.json in a clone dir: what was built there and how long it took.
// InstalledAt is the complete marker other clones copy node_modules from.
type Manifest struct {
	SHA         string             `json:"sha"`
	LockHash    string             `json:"lockHash"`
	NodeVersion string             `json:"nodeVersion,omitempty"`
	NodePath    string             `json:"nodePath,omitempty"`
	Install     string             `json:"install,omitempty"` // warm | copied from <clone> | npm ci
	InstalledAt time.Time          `json:"installedAt,omitzero"`
	PredevAt    time.Time          `json:"predevAt,omitzero"`
	UsedAt      time.Time          `json:"usedAt,omitzero"`
	Durations   map[string]float64 `json:"durations,omitempty"` // seconds, of the steps that actually ran
}

func manifestPath(dir string) string { return filepath.Join(dir, "manifest.json") }

func readManifest(dir string) (m Manifest) {
	if data, err := os.ReadFile(manifestPath(dir)); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if m.Durations == nil {
		m.Durations = map[string]float64{}
	}
	return m
}

func writeManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(manifestPath(dir), data)
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (s *Service) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := s.runner.Run(ctx, "", baseEnv(""), "git", append([]string{"-C", dir}, args...)...)
	return strings.TrimSpace(string(out)), err
}

func (s *Service) sh(ctx context.Context, dir string, env []string, script string) ([]byte, error) {
	return s.runner.Run(ctx, dir, env, "sh", "-c", script)
}

// prepareLeg makes <dataDir>/litmus/repos/<owner>__<repo>/<sha> ready to launch: a
// shared clone (never a worktree, so sessions inside the private AO cannot register
// worktrees in the user's .git), node_modules reused or installed, predev run.
func (s *Service) prepareLeg(ctx context.Context, repo string, r Recipe, leg, sha string, aka ...string) (Leg, error) {
	if !shaPattern.MatchString(sha) {
		return Leg{}, invalid("TESTING_SHA_INVALID", "not a commit SHA: "+sha)
	}
	local, err := s.localCheckout(ctx, append([]string{repo}, aka...)...)
	if err != nil {
		return Leg{}, err
	}
	nodeBin, nodeVersion, err := s.probeNode(ctx, r.Toolchain.Node)
	if err != nil {
		return Leg{}, err
	}
	rev := func() (string, error) { return s.git(ctx, local, "rev-parse", "--verify", "--quiet", sha+"^{commit}") }
	full, err := rev()
	if err != nil { // the checkout may predate the PR's commits
		if _, ferr := s.git(ctx, local, "fetch", "origin", sha); ferr == nil {
			full, err = rev()
		}
	}
	if err != nil {
		return Leg{}, apierr.Unavailable("TESTING_SHA_MISSING", fmt.Sprintf("commit %s is not in %s and could not be fetched", sha, local))
	}

	dir := filepath.Join(s.store.reposDir(repo), full)
	m := readManifest(dir)
	took := func(step string, since time.Time) {
		m.Durations[step] = time.Since(since).Round(time.Millisecond).Seconds()
	}
	t := time.Now()
	_, gitErr := os.Stat(filepath.Join(dir, ".git"))
	if head, err := s.git(ctx, dir, "rev-parse", "HEAD"); gitErr != nil || err != nil || head != full {
		_ = os.RemoveAll(dir) // absent, or an interrupted clone
		m = Manifest{Durations: map[string]float64{}}
		if _, err := s.runner.Run(ctx, "", baseEnv(""), "git", "clone", "--shared", "--no-checkout", local, dir); err != nil {
			return Leg{}, err
		}
		if _, err := s.git(ctx, dir, "checkout", "--detach", full); err != nil {
			return Leg{}, err
		}
		took("clone", t)
	}
	m.SHA, m.NodePath, m.NodeVersion, m.UsedAt = full, nodeBin, nodeVersion, s.clock()
	env := baseEnv("")
	if nodeBin != "" {
		env = baseEnv(filepath.Dir(nodeBin))
	}

	if r.Install.Cmd != "" {
		hash, err := lockHash(dir, r.Install.CacheKey, r.Toolchain.Node)
		if err != nil {
			return Leg{}, err
		}
		t = time.Now()
		if err := s.install(ctx, r, dir, hash, env, &m); err != nil {
			return Leg{}, err
		}
		if m.Install != "warm" {
			took("install", t)
		}
		if err := writeManifest(dir, m); err != nil {
			return Leg{}, err
		}
	}
	if r.Build.Cmd != "" && m.PredevAt.IsZero() {
		t = time.Now()
		if _, err := s.sh(ctx, filepath.Join(dir, r.Build.Dir), env, r.Build.Cmd); err != nil {
			return Leg{}, err
		}
		m.PredevAt = s.clock()
		took("predev", t)
	}
	return Leg{Name: leg, Worktree: dir, BuildManifest: manifestPath(dir), State: "prepared"}, writeManifest(dir, m)
}

// pruneClone forgets the worktrees of a shared clone whose directories are gone and deletes the ao/litmus-*
// branches no remaining worktree has checked out. The private AO that ran inside an earlier leg registered
// both in this clone; left behind they make a later spawn fail with BRANCH_CHECKED_OUT_ELSEWHERE.
func (s *Service) pruneClone(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	if !statable(filepath.Join(dir, ".git")) {
		return nil // the clone itself is gone (see Prune)
	}
	if _, err := s.git(ctx, dir, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune worktrees of %s: %w", filepath.Base(dir), err)
	}
	listed, err := s.git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("list worktrees of %s: %w", filepath.Base(dir), err)
	}
	branches, err := s.git(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/ao/litmus-*")
	if err != nil {
		return fmt.Errorf("list branches of %s: %w", filepath.Base(dir), err)
	}
	checkedOut := map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		if b, ok := strings.CutPrefix(strings.TrimSpace(line), "branch refs/heads/"); ok {
			checkedOut[b] = true
		}
	}
	var stale []string
	for _, b := range strings.Fields(branches) {
		if !checkedOut[b] {
			stale = append(stale, b)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := s.git(ctx, dir, append([]string{"branch", "-D"}, stale...)...); err != nil {
		return fmt.Errorf("delete branches of %s: %w", filepath.Base(dir), err)
	}
	return nil
}

// lockHash keys the node_modules cache: the cacheKey files' contents plus the node major.
func lockHash(dir string, keys []string, major string) (string, error) {
	h := sha256.New()
	for _, k := range keys {
		data, err := os.ReadFile(filepath.Join(dir, k))
		if err != nil {
			return "", fmt.Errorf("recipe cacheKey: %w", err)
		}
		h.Write(data)
	}
	return fmt.Sprintf("%x-node%s", h.Sum(nil), major), nil
}

// install leaves a complete node_modules in dir: already there (warm), copy-on-write
// copied from a sibling clone with the same lock hash, or a fresh install.
func (s *Service) install(ctx context.Context, r Recipe, dir, hash string, env []string, m *Manifest) error {
	modules := filepath.Join(dir, r.Install.Dir, "node_modules")
	if _, err := os.Stat(modules); err == nil && m.LockHash == hash && !m.InstalledAt.IsZero() {
		m.Install = "warm"
		return nil
	}
	m.LockHash, m.PredevAt = hash, time.Time{}
	if donor := findDonor(filepath.Dir(dir), dir, hash, r.Install.Dir); donor != "" {
		for _, flags := range [][]string{{"-c", "-R"}, {"-R"}} { // -c clones on APFS; plain copy elsewhere
			_ = os.RemoveAll(modules)
			if _, err := s.runner.Run(ctx, "", nil, "cp", append(flags, filepath.Join(donor, r.Install.Dir, "node_modules"), modules)...); err == nil {
				m.Install, m.InstalledAt = "copied from "+filepath.Base(donor), s.clock()
				return nil
			}
		}
		_ = os.RemoveAll(modules)
	}
	if _, err := s.sh(ctx, filepath.Join(dir, r.Install.Dir), env, r.Install.Cmd); err != nil {
		return err
	}
	m.Install, m.InstalledAt = "npm ci", s.clock()
	return nil
}

// findDonor returns a sibling clone whose node_modules is complete and has the same hash.
func findDonor(reposDir, self, hash, installDir string) string {
	entries, _ := os.ReadDir(reposDir)
	for _, e := range entries {
		d := filepath.Join(reposDir, e.Name())
		if m := readManifest(d); d != self && m.LockHash == hash && !m.InstalledAt.IsZero() {
			if fi, err := os.Stat(filepath.Join(d, installDir, "node_modules")); err == nil && fi.IsDir() {
				return d
			}
		}
	}
	return ""
}

// Prepare clones and builds the head, then the base, sequentially.
func (s *Service) Prepare(ctx context.Context, sessionID string) (Run, error) {
	run, done, err := s.begin(sessionID, "prepare")
	if err != nil {
		return Run{}, err
	}
	defer done()
	if run.HeadSHA == "" || run.BaseSHA == "" || (run.State != RunIntake && run.State != RunPrepared) {
		return Run{}, conflict("TESTING_RUN_STATE", fmt.Sprintf("run %s is %s with head %q; prepare needs a pinned run that is not launched yet", run.ID, run.State, run.HeadSHA))
	}
	recipe, err := s.store.LoadRecipe(run.Repo)
	if err != nil {
		return Run{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute) // a CLI timeout must not kill an install halfway
	defer cancel()
	var legs []Leg
	for _, l := range []struct{ name, sha string }{{"head", run.HeadSHA}, {"base", run.BaseSHA}} {
		leg, err := s.prepareLeg(ctx, run.Repo, recipe, l.name, l.sha, run.RequestedRepo)
		if err != nil {
			return Run{}, err
		}
		if err := s.pruneClone(ctx, leg.Worktree); err != nil { // best effort: a stale record only matters if a spawn trips on it
			slog.Warn("litmus: could not prune the shared clone", "run", run.ID, "leg", l.name, "error", err)
		}
		legs = append(legs, leg)
	}
	return s.update(run.ID, func(cur *Run) { cur.Legs, cur.State, cur.Error = legs, RunPrepared, "" })
}

// statable reports whether p can be stat-ed, whatever it is.
func statable(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
