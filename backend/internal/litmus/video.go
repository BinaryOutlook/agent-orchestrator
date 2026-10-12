package litmus

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	homebrewFFmpeg = "/opt/homebrew/bin/ffmpeg"
	recordTimeout  = 10 * time.Second // `record start` hangs when the window is not frontmost; never block a run on video
	stopTimeout    = 30 * time.Second
	videoTimeout   = 2 * time.Minute
	recordFPS      = "10" // a lower capture rate keeps the encoder from falling behind on a loaded Mac (it dropped a 3-minute recording at 12)
	noVideo        = "none (no recording and no after-shots to build a slideshow from)"
)

// Media runs ffmpeg with the given arguments. Tests substitute a fake, so no unit test encodes video.
type Media interface {
	Run(ctx context.Context, args ...string) error
}

type execMedia struct{}

func (execMedia) Run(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		bin = homebrewFFmpeg // the daemon's PATH may lack Homebrew
	}
	cmd := process.CommandContext(ctx, bin, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	errOut := &capWriter{max: 64 << 10}
	cmd.Stderr, cmd.WaitDelay = errOut, 2*time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLines(errOut.buf, 5))
	}
	return nil
}

// recordingPath is where a leg's `record start` writes; cutPath is the captioned video built for the leg.
func recordingPath(runDir, leg string) string { return filepath.Join(runDir, leg, leg+".mp4") }
func cutPath(runDir, leg string) string       { return filepath.Join(runDir, leg+"-cut.mp4") }

// startRecording asks the leg's own engine to record its window to <runDir>/<leg>/<leg>.mp4, so each video follows the
// window it belongs to, with the pointer overlay (--cursor) so receipts show where a click landed. If the engine refuses
// the overlay, it restarts without it and says so in <leg>/engine.log. It returns the leg with RecordStartedAt (taken
// just before the `record start` that took effect) and RecordCursor set; a failure or timeout is logged, never fatal.
func (s *Service) startRecording(ctx context.Context, runID string, leg Leg) Leg {
	e := s.engineFor(runID, leg)
	e.mu.Lock()
	defer e.mu.Unlock()
	path := recordingPath(filepath.Join(s.store.root, runID), leg.Name)
	began := s.clock()
	_, err := e.Run(ctx, recordTimeout, "record", "start", path, "--cursor", "--fps", recordFPS)
	leg.RecordCursor, leg.RecordFPS = err == nil, recordFPS
	if err != nil {
		s.engineLog(runID, leg.Name, "record start --cursor failed, restarting the recording without the pointer overlay: "+clip(cell(err.Error()), 300))
		began = s.clock()
		_, err = e.Run(ctx, recordTimeout, "record", "restart", path, "--fps", recordFPS) // restart: the failed start may have left a recording half open
	}
	if err != nil {
		slog.Warn("litmus: record start failed; continuing without a recording", "run", runID, "leg", leg.Name, "error", err)
		return leg
	}
	leg.RecordStartedAt, leg.RecordStoppedAt = began, time.Time{}
	return leg
}

// engineLog appends a line to <runDir>/<leg>/engine.log, where decisions about a leg's engine are kept.
func (s *Service) engineLog(runID, leg, line string) {
	dir := filepath.Join(s.store.root, runID, leg)
	if os.MkdirAll(dir, 0o750) != nil {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "engine.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%s %s\n", s.clock().Format(time.RFC3339), line)
		_ = f.Close()
	}
}

// stopRecording ends every recording the run's legs started and the persisted state says is still going (started, not
// stopped), once, and saves RecordStoppedAt. A leg whose engine is gone, or whose `record stop` fails, has its file
// dropped: it has no index and will not play, so the leg falls back to the slideshow. It returns the run with that state.
func (s *Service) stopRecording(ctx context.Context, run Run) Run {
	runID := run.ID
	stopped := map[string]time.Time{}
	for _, l := range run.Legs {
		if l.RecordStartedAt.IsZero() || !l.RecordStoppedAt.IsZero() {
			continue
		}
		stopped[l.Name] = s.clock()
		path := recordingPath(filepath.Join(s.store.root, runID), l.Name)
		if l.Engine == nil {
			_ = os.Remove(path)
			continue
		}
		e := s.engineFor(runID, l)
		e.mu.Lock()
		_, err := e.Run(ctx, stopTimeout, "record", "stop")
		e.mu.Unlock()
		if err != nil {
			slog.Warn("litmus: record stop failed; dropping the unfinished recording", "run", runID, "leg", l.Name, "error", err)
			s.engineLog(runID, l.Name, "record stop failed, recording dropped: "+clip(cell(err.Error()), 300))
			_ = os.Remove(path)
		}
	}
	if len(stopped) == 0 {
		return run
	}
	out, err := s.update(runID, func(cur *Run) {
		for i, l := range cur.Legs {
			if at, ok := stopped[l.Name]; ok && cur.Legs[i].RecordStoppedAt.IsZero() {
				cur.Legs[i].RecordStoppedAt = at
			}
		}
	})
	if err != nil {
		slog.Warn("litmus: could not save the recording state", "run", runID, "error", err)
		return run
	}
	return out
}

// legVideo is what the video step left for one leg: raw is its recording (<leg>.mp4), cut is <leg>-cut.mp4.
// A cut without a recording is the slideshow of the leg's after-shots.
type legVideo struct {
	leg      string
	raw, cut bool
}

func (v legVideo) files() (out []string) {
	if v.raw {
		out = append(out, v.leg+".mp4")
	}
	if v.cut {
		out = append(out, v.leg+"-cut.mp4")
	}
	return out
}

func (v legVideo) String() string {
	switch {
	case v.raw && v.cut:
		return fmt.Sprintf("%s: recorded (%s.mp4; %s-cut.mp4 keeps a 2 s window around each action)", v.leg, v.leg, v.leg)
	case v.raw:
		return fmt.Sprintf("%s: recorded (%s.mp4)", v.leg, v.leg)
	}
	return fmt.Sprintf("%s: live recording lost, slideshow attached (%s-cut.mp4, the after-shots at 1 fps)", v.leg, v.leg)
}

// videoLine is the verdict's Video: text, one entry per leg that has video.
func videoLine(vs []legVideo) string {
	if len(vs) == 0 {
		return noVideo
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = v.String()
	}
	return strings.Join(parts, "; ")
}

// finishVideo builds each leg's cut from that leg's own recording and only that leg's actions: a 2 s window around
// each action, captioned. A leg without a recording gets a 1 fps slideshow of its after-shots instead.
// ffmpeg failures are logged: the verdict never waits on video.
func (s *Service) finishVideo(ctx context.Context, runDir string, run Run) (out []legVideo) {
	ctx, cancel := context.WithTimeout(ctx, videoTimeout)
	defer cancel()
	all := readActions(runDir)
	for _, leg := range []string{"head", "base"} {
		acts := slices.DeleteFunc(slices.Clone(all), func(a actionRec) bool { return cmp.Or(a.Leg, "head") != leg })
		var l Leg
		if i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg }); i >= 0 {
			l = run.Legs[i]
		}
		started := l.RecordStartedAt
		v, raw, cut := legVideo{leg: leg}, recordingPath(runDir, leg), cutPath(runDir, leg)
		st, err := os.Stat(raw)
		playable := err == nil && st.Size() > 0
		if playable && (started.IsZero() || l.RecordStoppedAt.IsZero()) { // never finished (the daemon restarted): no index, will not play
			_ = os.Remove(raw)
			playable = false
		}
		if playable {
			v.raw = true
			if segs := segments(acts, started); len(segs) > 0 {
				v.cut = s.encode(ctx, runDir, leg+"-cut-captions.srt", captions(segs), func(srt string) []string { return cutArgs(raw, srt, cut, segs) }) == nil
			}
		} else {
			v.cut = s.slideshow(ctx, runDir, leg, acts)
		}
		if v.raw || v.cut {
			out = append(out, v)
		}
	}
	return out
}

// slideshow turns a leg's after-shots into <leg>-cut.mp4 and reports whether it was made.
func (s *Service) slideshow(ctx context.Context, runDir, leg string, acts []actionRec) bool {
	var list strings.Builder
	var segs []seg
	file := func(path string) { fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(path, "'", `'\''`)) }
	last := ""
	for _, a := range acts {
		if a.Action == "test" { // a package test run has no screenshot
			continue
		}
		if st, err := os.Stat(a.After); a.After != "" && err == nil && st.Mode().IsRegular() {
			at := time.Duration(len(segs)) * time.Second
			segs, last = append(segs, seg{at, at + time.Second, caption(a)}), a.After
			file(a.After)
			list.WriteString("duration 1\n")
		}
	}
	if last == "" {
		return false
	}
	file(last) // the concat demuxer ignores the last duration, so the last still is listed twice
	listPath := filepath.Join(runDir, "slides-"+leg+".txt")
	if os.WriteFile(listPath, []byte(list.String()), 0o600) != nil {
		return false
	}
	return s.encode(ctx, runDir, leg+"-slide-captions.srt", captions(segs), func(srt string) []string { return slideshowArgs(listPath, srt, cutPath(runDir, leg)) }) == nil
}

// encode writes the caption file and runs ffmpeg with the arguments build returns for it.
func (s *Service) encode(ctx context.Context, runDir, srtName, srt string, build func(srtPath string) []string) error {
	path := filepath.Join(runDir, srtName)
	err := os.WriteFile(path, []byte(srt), 0o600)
	if err == nil {
		err = s.media.Run(ctx, build(path)...)
	}
	if err != nil {
		slog.Warn("litmus: video step failed", "run", filepath.Base(runDir), "error", err)
	}
	return err
}

// slideshowArgs turns a concat list of stills into a 1280x720 1 fps H.264 file with a soft caption track
// (this ffmpeg has no drawtext filter, so captions cannot be burned in).
func slideshowArgs(list, srt, out string) []string {
	return []string{"-y", "-f", "concat", "-safe", "0", "-i", list, "-i", srt, "-map", "0:v", "-map", "1:s",
		"-vf", "scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,fps=1,format=yuv420p",
		"-c:v", "libx264", "-c:s", "mov_text", "-movflags", "+faststart", out}
}

// seg is a stretch of video and its caption.
type seg struct {
	from, to time.Duration
	text     string
}

// segments gives each action a 2 s window of the recording (1 s either side) and merges windows that touch.
func segments(acts []actionRec, start time.Time) (out []seg) {
	for _, a := range acts {
		if a.Action == "test" { // a package test run is not a moment on screen
			continue
		}
		at := a.At.Sub(start)
		if at < 0 {
			continue
		}
		from, to := max(at-time.Second, 0), at+time.Second
		if n := len(out); n > 0 && from <= out[n-1].to {
			out[n-1].to, out[n-1].text = to, out[n-1].text+" / "+caption(a)
		} else {
			out = append(out, seg{from, to, caption(a)})
		}
	}
	return out
}

// caption is the line under a step in the video: Step 3: click "Collapse sidebar".
func caption(a actionRec) string {
	return fmt.Sprintf("Step %d: %s", a.Seq, stepText(a.Action, a.Args, a.TargetRole, a.TargetName))
}

// refInfo is what a snapshot line says about an element: its role and accessible name.
type refInfo struct{ role, name string }

var snapRef = regexp.MustCompile(`^\s*- ([A-Za-z]+)(?: ("(?:[^"\\]|\\.)*"))?[^\n]*?\bref=(e[0-9]+)`)

// parseRefs reads `snapshot` output (`- button "Go back" [ref=e7]`) into refs keyed the way actions spell them (@e7).
func parseRefs(snap string) map[string]refInfo {
	out := map[string]refInfo{}
	for _, l := range strings.Split(snap, "\n") {
		if m := snapRef.FindStringSubmatch(l); m != nil {
			name, _ := strconv.Unquote(m[2])
			out["@"+m[3]] = refInfo{m[1], name}
		}
	}
	return out
}

var roleWords = map[string]string{"button": "a button", "link": "a link", "textbox": "a text field", "combobox": "a field", "checkbox": "a checkbox", "tab": "a tab", "menuitem": "a menu item"}

// plainName is an element's accessible name as a reader should see it: a project row's name is joined to its path
// and age, so only the first line is kept, cut at the first word that is a local path.
func plainName(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	isPath := func(w string) bool {
		return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") || strings.Contains(w, "/Users/") || strings.Contains(w, "/home/")
	}
	words := strings.Fields(s)
	if i := slices.IndexFunc(words, isPath); i > 0 {
		words = words[:i]
	}
	return strings.Join(slices.DeleteFunc(words, isPath), " ")
}

// stepText says what an action did in the words a reader would use: click "Go back", type "hi" into "Message the
// agent", press Enter. An element the snapshot did not name is "a button" (or "an element" with no role either).
func stepText(action string, args []string, role, name string) string {
	quote := func(s string) string {
		return `"` + clip(strings.ReplaceAll(strings.Join(strings.Fields(s), " "), `"`, "'"), 40) + `"`
	}
	target := cmp.Or(roleWords[role], "an element")
	if name = plainName(name); name != "" {
		target = quote(name)
	}
	text := ""
	if len(args) > 1 {
		text = quote(args[1])
	}
	switch action {
	case "press":
		return "press " + strings.Join(args, " ")
	case "fill", "type":
		return "type " + cmp.Or(text, "text") + " into " + target
	case "select":
		return "select " + cmp.Or(text, "an option") + " in " + target
	}
	return action + " " + target
}

// cutArgs joins the segments of in into out, one seeked input per segment, with the captions as a soft track.
func cutArgs(in, srt, out string, segs []seg) []string {
	args, graph := []string{"-y"}, ""
	for i, sg := range segs {
		args = append(args, "-ss", fmt.Sprintf("%.3f", sg.from.Seconds()), "-t", fmt.Sprintf("%.3f", (sg.to-sg.from).Seconds()), "-i", in)
		graph += fmt.Sprintf("[%d:v]", i)
	}
	graph += fmt.Sprintf("concat=n=%d:v=1:a=0,format=yuv420p[v]", len(segs))
	return append(args, "-i", srt, "-filter_complex", graph, "-map", "[v]", "-map", fmt.Sprintf("%d:s", len(segs)),
		"-c:v", "libx264", "-c:s", "mov_text", "-movflags", "+faststart", out)
}

// captions renders segs as SRT on the output timeline: each segment starts where the previous one ended.
func captions(segs []seg) string {
	var b strings.Builder
	var at time.Duration
	for i, sg := range segs {
		d := sg.to - sg.from
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(at), srtTime(at+d), sg.text)
		at += d
	}
	return b.String()
}

func srtTime(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000)
}
