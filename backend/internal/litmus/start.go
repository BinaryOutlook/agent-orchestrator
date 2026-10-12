package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Harness is the only agent a testing worker may run on: its tool lists and
// dontAsk mode are enforced by that adapter alone.
const Harness = "claude-code"

// SpawnRequest is what the daemon needs to launch one testing worker.
type SpawnRequest struct {
	ProjectID string
	Harness   string
	Name      string // sidebar label
	Prompt    string // first message
}

// Spawner launches the testing worker's AO session (kind worker, mode tui) and returns its id.
type Spawner func(ctx context.Context, req SpawnRequest) (sessionID string, err error)

// ShellRule is the sentence the spawn prompt and SKILL.md share: what the tester's open toolset is for, and its limits.
const ShellRule = "You have a normal shell and the usual tools: run any command, read code and logs. Put scratch files in `scratch/`; never edit the files Litmus writes in the run folder. Build, open and drive the two app windows only through `ao testing`, leave the checkouts as Litmus made them, and never push or post to GitHub."

// ExploreRule is the sentence that tells the tester it is driving a real app and may reach for settings.
const ExploreRule = "Explore the app the way a user would: menus, settings, modes. If a feature hides behind a setting, turn the setting on and say so in your note."

// StartRequest names the PR to test. An empty Harness means claude-code.
type StartRequest struct {
	ProjectID  string
	Harness    string
	PRURL      string
	NoComment  bool // the run's verdict is not posted to the PR
	NoEvidence bool // no screenshots or GIFs are published for the comment
}

// Started is the session a run will belong to; the worker makes the run itself with `ao testing intake`.
type Started struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// WithSpawner sets how Start launches workers.
func (s *Service) WithSpawner(sp Spawner) *Service {
	s.spawn = sp
	return s
}

// Start spawns a testing worker for a PR. It holds no run: the worker's intake does.
func (s *Service) Start(ctx context.Context, req StartRequest) (Started, error) {
	repo, number, err := ParsePR(req.PRURL)
	if err != nil {
		return Started{}, invalid("TESTING_PR_URL_INVALID", "start needs a GitHub pull request URL (https://github.com/O/R/pull/N) or O/R#N")
	}
	harness := strings.TrimSpace(req.Harness)
	if harness == "" {
		harness = Harness
	}
	if harness != Harness {
		return Started{}, invalid("TESTING_HARNESS_UNSUPPORTED", fmt.Sprintf("testing workers run on %s only, not %q", Harness, harness))
	}
	if s.spawn == nil {
		return Started{}, conflict("TESTING_SPAWN_UNAVAILABLE", "this daemon cannot spawn testing workers")
	}
	if s.resolve != nil { // refuse here, not three tool calls into the worker's run
		if _, err := s.localCheckout(ctx, repo); err != nil {
			return Started{}, err
		}
	}
	url := fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
	name := fmt.Sprintf("litmus: PR #%d", number)
	id, err := s.spawn(ctx, SpawnRequest{
		ProjectID: strings.TrimSpace(req.ProjectID),
		Harness:   harness,
		Name:      name,
		Prompt:    fmt.Sprintf("Test PR %s. Start with `ao testing intake %s`. %s %s", url, url, ShellRule, ExploreRule),
	})
	if err != nil {
		return Started{}, err
	}
	for kind, off := range map[string]bool{"comment": req.NoComment, "evidence": req.NoEvidence} { // the worker's intake picks the modes up by session
		if off {
			if err := s.store.markOff(kind, id); err != nil {
				return Started{}, fmt.Errorf("session %s started but its no-%s mode was not saved, so the run may not honor it: %w", id, kind, err)
			}
		}
	}
	if err := s.store.setSessionProject(id, req.ProjectID); err != nil {
		return Started{}, fmt.Errorf("session %s started but its project was not saved, so its run will not be listed under the project: %w", id, err)
	}
	if err := s.store.markSpawned(id, spawned{At: s.clock().UTC(), Repo: repo, PR: number}); err != nil {
		return Started{}, fmt.Errorf("session %s started but its spawn time was not saved, so it will not be reaped if it never runs intake: %w", id, err)
	}
	return Started{SessionID: id, Name: name}, nil
}

// spawned is what the sweep keeps of a started tester, until its intake or its reaping: <dataDir>/litmus/spawned/<sessionID>.
type spawned struct {
	At   time.Time `json:"at"`
	Repo string    `json:"repo"`
	PR   int       `json:"pr"`
}

func (s *Store) spawnedDir() string { return filepath.Join(filepath.Dir(s.root), "spawned") }

func (s *Store) markSpawned(sessionID string, sp spawned) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || filepath.Base(sessionID) != sessionID {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	if err := os.MkdirAll(s.spawnedDir(), 0o750); err != nil {
		return err
	}
	data, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.spawnedDir(), sessionID), data)
}

// spawnedSessions lists the testers started and not yet settled, by session id.
func (s *Store) spawnedSessions() (map[string]spawned, error) {
	entries, err := os.ReadDir(s.spawnedDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]spawned{}
	for _, e := range entries {
		var sp spawned
		if data, err := os.ReadFile(filepath.Join(s.spawnedDir(), e.Name())); err == nil && json.Unmarshal(data, &sp) == nil {
			out[e.Name()] = sp
		}
	}
	return out, nil
}

// spawnedSession reads one tester's spawn marker.
func (s *Store) spawnedSession(sessionID string) (sp spawned, found bool) {
	if sessionID == "" || filepath.Base(sessionID) != sessionID {
		return sp, false
	}
	data, err := os.ReadFile(filepath.Join(s.spawnedDir(), sessionID))
	return sp, err == nil && json.Unmarshal(data, &sp) == nil
}

func (s *Store) clearSpawned(sessionID string) {
	_ = os.Remove(filepath.Join(s.spawnedDir(), sessionID))
}
