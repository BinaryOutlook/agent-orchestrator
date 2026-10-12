package litmus

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Comment modes, set on the run by intake.
const (
	CommentPost = "post"
	CommentOff  = "off"
)

const (
	commentCap     = 6000 // GitHub allows far more; a PR comment this long is not read
	commentTimeout = 60 * time.Second
)

// commentFit is one squeeze of the body: how long a table note, a Limits bullet and a Notes entry may be, and how many
// bullets show. A fit with no notes length has no Notes section, which carries each note in full.
type commentFit struct{ note, limit, bullets, notes int }

// commentFits run from roomy to tight; the table rows are clipped last.
var commentFits = []commentFit{{80, 300, 12, 600}, {80, 150, 6, 300}, {80, 80, 3, 150}, {60, 60, 2, 0}, {40, 40, 1, 0}}

func commentMarker(repo string, n int) string {
	return fmt.Sprintf("<!-- litmus:comment %s#%d -->", repo, n)
}

// mdText makes tester-written text safe for a public comment: one line, clipped, no raw HTML or entities, no
// link or image syntax, no engine refs (@e12 would also ping a user) and no @mention.
func mdText(s string, n int) string {
	s = cell(strings.ReplaceAll(clip(engineRef.ReplaceAllString(strings.Join(strings.Fields(s), " "), "that element"), n), `\`, `\\`))
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", "[", `\[`, "@", "@\u200b").Replace(s)
}

var (
	engineRef = regexp.MustCompile(`(?:\bref[ =]+)?\B@e\d+\b`)
	legPrefix = regexp.MustCompile(`(?i)^(base|head)( leg)?:\s*`)
)

// plainSentence is a tester's note as one sentence of a public comment: no "Base:" prefix, clipped to n, ending in a stop.
func plainSentence(note string, n int) string {
	out := plainCell(note, n)
	if out == "" || strings.HasSuffix(out, "...") || strings.ContainsAny(out[len(out)-1:], "!?") {
		return out
	}
	return strings.TrimRight(out, ".") + "."
}

// shorten cuts text to n characters at a word, with "..." when it cut.
func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	s = s[:n-3]
	if i := strings.LastIndex(s, " "); i > n/2 {
		s = s[:i]
	}
	return s + "..."
}

var danglingWord = regexp.MustCompile(`(?i) (and|or|but|the|a|an|of|to|with|in|on|at|for|is|are)$`)

// clause is s as the end of a sentence: cut at a word when over n characters (never mid-word, never inside an
// unclosed bracket or after a word that wants another) and with no stop of its own, so the caller's period is the only one.
func clause(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		cut := string(r[:n])
		if r[n] != ' ' {
			if i := strings.LastIndex(cut, " "); i > 0 {
				cut = cut[:i]
			}
		}
		if i := strings.LastIndex(cut, "("); i >= 0 && !strings.Contains(cut[i:], ")") {
			cut = cut[:i]
		}
		s = danglingWord.ReplaceAllString(strings.TrimRight(cut, " "), "")
	}
	return strings.TrimRight(s, " .,;:")
}

// sentences is s cut back to a whole sentence when it is over n characters and has a stop inside them.
func sentences(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		if i := strings.LastIndex(string(r[:n]), ". "); i > 0 {
			return s[:i]
		}
	}
	return s
}

var testerWords = regexp.MustCompile(`(?i)\b(head|base|c\d+)\b`)

// plainTester is a tester's text in the reader's words, for a tester who ignored the rules: head is the PR build,
// base is the base branch, and a check id is that check's name.
func plainTester(s string, run Run, checks []Check) string {
	return testerWords.ReplaceAllStringFunc(legPrefix.ReplaceAllString(strings.TrimSpace(s), ""), func(w string) string {
		out := "that check"
		switch low := strings.ToLower(w); low {
		case "head":
			out = "the PR build"
		case "base":
			out = cmp.Or(run.BaseRef, "main")
		default:
			if i := slices.IndexFunc(checks, func(c Check) bool { return strings.EqualFold(c.ID, low) }); i >= 0 {
				out = strings.ReplaceAll(checks[i].Name, "-", " ")
			}
		}
		if first, _ := utf8.DecodeRuneInString(w); unicode.IsUpper(first) {
			out = recase(out, true)
		}
		return out
	})
}

// buildName is a lead-in or inner mention of a build, which the comment adds itself: "on the PR build", "in the PR build", "on main".
func buildName(run Run) string {
	return `(?:on|in) (?:the PR build|main|` + regexp.QuoteMeta(run.baseName()) + `)`
}

// builds strips the build a tester's note names, since checkPara and the captions lead with it: a leading
// "On the PR build," or "On main" (comma optional), and the same words inside the first clause (up to the first comma
// or period). A note that lost its opening gets a capital back.
func builds(note string, run Run) string {
	s, name := strings.TrimSpace(note), buildName(run)
	lead := regexp.MustCompile(`(?i)^` + name + `(?:,\s*|\s+|$)`)
	inner := regexp.MustCompile(`(?i)\s+` + name + `(\s|[,.;:]|$)`)
	cut := lead.ReplaceAllString(s, "")
	first, rest := cut, ""
	if i := strings.IndexAny(cut, ",."); i >= 0 {
		first, rest = cut[:i], cut[i:]
	}
	cut = inner.ReplaceAllString(first, "$1") + rest
	if cut == s {
		return s
	}
	return recase(strings.TrimSpace(cut), true)
}

// subjectLed reports whether a note (already through builds) opens with a build as its subject, "The PR build shows",
// "The main build is" or "Main has", so the comment keeps it as its own sentence instead of adding "On the PR build,".
func subjectLed(note string, run Run) bool {
	return regexp.MustCompile(`(?i)^(?:the (?:PR|main) build|main|` + regexp.QuoteMeta(run.baseName()) + `)(?:'s)?(?:\s|$)`).MatchString(note)
}

var compared = regexp.MustCompile(`(?i)\bcompared\b`)

// withoutComparison drops the sentences of a tester's note that say what was or was not compared with the base branch:
// the comment states that itself. The other sentences stay. Sentences end at . ! ? or ; before a space.
func withoutComparison(note string, run Run) string {
	main := regexp.MustCompile(`(?i)\b(?:main|` + regexp.QuoteMeta(run.baseName()) + `)\b`)
	var kept []string
	start, dropped := 0, 0
	for _, loc := range append(sentenceStop.FindAllStringIndex(note, -1), []int{len(note), len(note)}) {
		if s := note[start:loc[1]]; strings.TrimSpace(s) != "" {
			if compared.MatchString(s) && main.MatchString(s) {
				dropped++
			} else {
				kept = append(kept, strings.TrimSpace(s))
			}
		}
		start = loc[1]
	}
	out := strings.Join(kept, " ")
	if trimmed := strings.TrimRight(out, " ;,"); dropped > 0 && trimmed != out { // a dropped clause leaves its separator behind
		out = trimmed + "."
	}
	return out
}

var sentenceStop = regexp.MustCompile(`[.!?;](?:\s+|$)`)

// plainVerdict is v with the tester's own words (in each check's expectation and note, the skip reason and a blocked
// reason) said in the reader's. The checks are copied; v is not changed.
func plainVerdict(run Run, v Verdict) Verdict {
	checks := slices.Clone(v.Checks)
	for i := range checks {
		checks[i].Expect, checks[i].Note = plainTester(checks[i].Expect, run, v.Checks), builds(withoutComparison(plainTester(checks[i].Note, run, v.Checks), run), run)
	}
	if v.Overall == CheckBlocked { // another verdict's reasons begin with a check id and name, which the comment matches on
		v.Reasons = slices.Clone(v.Reasons)
		for i, r := range v.Reasons {
			v.Reasons[i] = plainTester(r, run, v.Checks)
		}
	}
	v.SkipBase = plainTester(v.SkipBase, run, v.Checks)
	v.Checks = checks
	return v
}

// recase raises or lowers the first letter; a lower-case request leaves acronyms and names (second letter capital) and "I" alone.
func recase(s string, up bool) string {
	r := []rune(s)
	if len(r) > 1 && up {
		r[0] = unicode.ToUpper(r[0])
	} else if len(r) > 1 && unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1]) && (r[0] != 'I' || r[1] != ' ') {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

func lowerFirst(s string) string { return recase(s, false) }

// leadWords are the words an expectation can open with that are not a name: lowering them is safe.
var leadWords = []string{"a", "an", "the", "after", "when", "with", "in", "on", "at", "if", "once", "every", "each", "both", "while", "before"}

// lowerLead lowers the first letter of an expectation that follows "that", unless the word is a label or a name.
// A function word or an -ing word ("typing", "opening") is always lowered. Any other word stays when it is all
// capitals or holds a digit, dot or slash (CLI, Orchestrator.inc, Local/Cloud), or when the same capitalised word
// turns up again, not at the start of a sentence, later in the expectation or in one of the others (Go in "Go back
// and Go forward"). Everything else is a common noun that only sat first: "Search field" becomes "search field".
func lowerLead(s string, others ...string) string {
	w, rest, _ := strings.Cut(s, " ")
	w = strings.Trim(w, `.,;:!?()"'`)
	if lw := strings.ToLower(w); slices.Contains(leadWords, lw) || (len(lw) > 4 && strings.HasSuffix(lw, "ing")) {
		return lowerFirst(s)
	}
	if w == "" || strings.ContainsAny(w, "0123456789./") || (len(w) > 1 && w == strings.ToUpper(w)) {
		return s
	}
	if capitalised(w, rest, true) || slices.ContainsFunc(others, func(o string) bool { return capitalised(w, o, false) }) {
		return s
	}
	return lowerFirst(s)
}

// capitalised reports whether w turns up in text anywhere but at the start of a sentence. mid says text itself
// begins mid-sentence (the rest of an expectation after its first word); otherwise text begins a sentence.
func capitalised(w, text string, mid bool) bool {
	for _, t := range strings.Fields(text) {
		if mid && strings.Trim(t, `.,;:!?()"'`) == w {
			return true
		}
		mid = !strings.ContainsAny(t[len(t)-1:], ".!?")
	}
	return false
}

// checkLabel names a check by what it expects, in 60 characters (its name, de-slugged, when there is no expectation).
func checkLabel(c Check) string {
	return mdText(shorten(cmp.Or(c.Expect, strings.ReplaceAll(c.Name, "-", " ")), 60), 80)
}

// rowLabel is the label of the table row c belongs to: a base check beside its head twin is named like the twin.
func rowLabel(checks []Check, c Check) string {
	if c.onLeg() == "base" {
		if i := slices.IndexFunc(checks, func(h Check) bool { return h.onLeg() == "head" && strings.EqualFold(h.Name, c.Name) }); i >= 0 {
			return checkLabel(checks[i])
		}
	}
	return checkLabel(c)
}

// cellWords are the table's words for a result.
var cellWords = map[string]string{"pass": "works", "fail": "fails", "partial": "partly", "skipped": "not reached", "observed": "looked at", "not compared on base": "not compared", "-": "not compared"}

func cellWord(s string) string { return cmp.Or(cellWords[s], s) }

// tally counts the head checks by what they showed.
type tally struct{ passed, shown, pre, prePartial, notCompared, failed, partial, unreached int }

func count(checks []Check) (t tally) {
	for _, c := range checks {
		if c.onLeg() != "head" {
			continue
		}
		base := baseResult(checks, c)
		switch {
		case c.State == CheckSkipped:
			t.unreached++
			continue
		case preExisting(checks, c):
			t.pre++
			if c.State == CheckPartial {
				t.prePartial++
			}
			continue
		case base == "" || base == string(CheckSkipped):
			t.notCompared++
		case c.State == CheckPartial:
			t.partial++
		}
		if c.State == CheckPass {
			t.passed++
			if base == string(CheckFail) {
				t.shown++
			}
		}
		if c.State == CheckFail {
			t.failed++
		}
	}
	return t
}

// verdictLine is the sentence after "Result:", from the overall verdict and what the checks showed.
func verdictLine(overall CheckState, t tally, main string) string {
	left := t.notCompared + t.partial + t.unreached
	switch {
	case overall == CheckBlocked:
		return "could not test this one"
	case overall == CheckFail || t.failed > 0:
		return "something broke; do not merge yet"
	case t.unreached > 0 && t.passed+t.pre+t.notCompared+t.failed+t.partial == 0:
		return "could not test this from the app"
	case t.passed == 0: // nothing failed, nothing was confirmed
		return "nothing failed, but I could not confirm the change from the app; read the diff before merging"
	case overall == CheckPartial && left == 0:
		return "the PR build works, but nothing differed from " + main + "; read the diff before merging"
	case left == 0:
		return "the PR does what it says; fine to merge from my side"
	case overall == CheckPartial:
		return "the PR build works as far as I could check; nothing was compared against " + main + ", so read the diff before merging"
	}
	return "the fix works; fine to merge from my side. " + leftover(t.notCompared+t.partial, t.unreached)
}

// leftover says what the checks that did not finish as a comparison came to: some only ran on the PR build, some could
// not be done at all (skipped, or never reached), or both.
func leftover(only, undone int) string {
	few := func(n int, one, many string) string { return map[bool]string{true: one, false: many}[n == 1] }
	switch {
	case only > 0 && undone > 0:
		return few(only, "One check only ran on the PR build", "A few checks only ran on the PR build") + " and " + few(undone, "one could not be done", "a few could not be done")
	case only > 0:
		return few(only, "One check only ran on the PR build", "A few checks only ran on the PR build")
	}
	return few(undone, "One check could not be done", "A few checks could not be done")
}

// plainReason says a derive reason in the comment's words; a tester's own reason passes through.
func plainReason(r, main string) string {
	switch r {
	case reasonPreOnly:
		return "Whatever failed on the PR build failed on " + main + " too."
	case reasonPrePartial:
		return "What I could not confirm on the PR build I could not confirm on " + main + " either."
	case reasonPreBoth:
		return plainReason(reasonPreOnly, main) + " " + plainReason(reasonPrePartial, main)
	case reasonNoneReached:
		return "I could not reach any check on the PR build."
	case reasonNoDifference:
		return "Nothing passed on the PR build that failed on " + main + ", so I never saw the difference."
	}
	return mdText(r, 300)
}

var outcomeWords = map[CheckState]string{CheckPass: "it worked", CheckFail: "it did not work", CheckPartial: "it only partly worked"}

// checkPara tells one check the way a teammate would: what was tried, what happened on the PR build, then on main.
func checkPara(run Run, v Verdict, c Check, n, i int) string {
	notes := make([]string, 0, 2*len(v.Checks)) // every note and expectation: a word capitalised mid-sentence there is a label
	for _, x := range v.Checks {
		notes = append(notes, x.Note, x.Expect)
	}
	label, main := lowerLead(mdText(clause(cmp.Or(c.Expect, strings.ReplaceAll(c.Name, "-", " ")), 160), 400), notes...), run.baseName()
	if c.State == CheckSkipped {
		return strings.TrimSpace(fmt.Sprintf("I could not check that %s. %s", label, recase(plainSentence(c.Note, n), true)))
	}
	say := func(who string, x Check) string {
		note := plainSentence(x.Note, n)
		if subjectLed(note, run) {
			return recase(note, true)
		}
		return fmt.Sprintf("On %s, %s", who, cmp.Or(lowerLead(note, notes...), outcomeWords[x.State]+"."))
	}
	out := []string{[]string{"I checked that ", "Next I checked that ", "I also checked that "}[i%3] + label + "."}
	if c.onLeg() == "head" {
		out = append(out, say("the PR build", c))
	}
	switch twin := baseTwin(v.Checks, c); {
	case c.onLeg() == "base":
		out = append(out, say(main, c))
	case twin >= 0 && v.Checks[twin].State == CheckSkipped:
		out = append(out, "I did not repeat this on "+main+skipWhy(v.Checks[twin].Note)+".")
	case twin >= 0:
		out = append(out, say(main, v.Checks[twin]))
	case baseResult(v.Checks, c) != "":
		out = append(out, "I also looked at it on "+main+".")
	case v.SkipBase != "": // said once, after the result line
	default:
		out = append(out, "I could not compare it with "+main+".")
	}
	switch {
	case c.onLeg() != "head":
	case preExisting(v.Checks, c) && c.State == CheckPartial && baseResult(v.Checks, c) == string(CheckPartial): // the main sentence already says what was seen
	case preExisting(v.Checks, c):
		out = append(out, "The same thing happens on "+main+", so the PR did not cause it.")
	case baseResult(v.Checks, c) == string(CheckFail):
		out = append(out, "That is the fix, and it holds.")
	}
	return strings.Join(out, " ")
}

// skipWhy is ": <why>" for the note on a check the tester did not repeat on the base, or "" when it gave none.
func skipWhy(note string) string {
	if why := lowerFirst(mdText(clause(note, 200), 400)); why != "" {
		return ": " + why
	}
	return ""
}

// took is how long the run lasted, in whole minutes.
func took(d time.Duration) string {
	switch m := int(d.Round(time.Minute) / time.Minute); {
	case m < 1:
		return "Took under a minute."
	case m == 1:
		return "Took 1 minute."
	default:
		return fmt.Sprintf("Took %d minutes.", m)
	}
}

// renderComment is the PR comment for a stored verdict, ending in the marker that finds it again. It is built from
// the run, the verdict and the published evidence alone and fits commentCap by squeezing everything before the
// table notes. The image lines do not count against the cap; a body that still does not fit loses them.
func renderComment(run Run, v Verdict, ev evidence, now time.Time) string {
	v = plainVerdict(run, v)
	marker := commentMarker(run.Repo, run.PRNumber)
	images := ev.imageChars()
	for _, f := range commentFits {
		if body := commentBody(run, v, ev, now, f) + "\n\n" + marker; len(body)-images <= commentCap {
			return body
		}
	}
	body := commentBody(run, v, evidence{limits: ev.limits}, now, commentFits[len(commentFits)-1]) + "\n\n" + marker
	if len(body) <= commentCap {
		return body
	}
	return strings.ToValidUTF8(body[:commentCap-len(marker)-6], "") + "...\n\n" + marker
}

func commentBody(run Run, v Verdict, ev evidence, now time.Time, f commentFit) string {
	var blocks []string // paragraphs, joined by a blank line
	add := func(format string, a ...any) { blocks = append(blocks, fmt.Sprintf(format, a...)) }
	main, n := run.baseName(), max(f.notes, f.note)
	var rows []Check
	for _, c := range v.Checks {
		if _, _, beside := compareCells(v.Checks, c); !beside {
			rows = append(rows, c)
		}
	}
	both := slices.ContainsFunc(v.Checks, func(c Check) bool {
		return c.onLeg() == "base" || slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == "base" })
	})
	add("Litmus tried #%d on the PR build%s.", run.PRNumber, map[bool]string{true: " and on " + main}[both])
	add("**Result: %s.**", verdictLine(v.Overall, count(v.Checks), main))
	if why := lowerFirst(mdText(clause(v.SkipBase, 200), 400)); why != "" {
		add("I did not run %s: %s.", main, why)
	}
	if len(rows) > 4 { // a handful of checks read better as paragraphs; a long list also gets a table to scan
		table := make([]string, 0, 2+len(rows))
		table = append(table, "| what was checked | PR build | "+main+" | note |", "|---|---|---|---|")
		for _, c := range rows {
			head, base, _ := compareCells(v.Checks, c)
			table = append(table, fmt.Sprintf("| %s | %s | %s | %s |", checkLabel(c), cellWord(head), cellWord(base), plainCell(c.Note, f.note)))
		}
		add("%s", strings.Join(table, "\n"))
	}
	for i, c := range rows {
		add("%s", checkPara(run, v, c, n, i))
	}
	for _, it := range ev.items {
		if it.kind == kindHero {
			add("%s\n%s", it.image(), it.caption)
		}
	}
	if line := areaLine(run.Areas, v.Checks); line != "" {
		add("%s", line)
	}
	more, vids := slices.DeleteFunc(slices.Clone(ev.items), func(it evItem) bool { return it.kind == kindHero }), videoParts(run, ev)
	if len(more)+len(vids) > 0 {
		slices.SortStableFunc(more, func(a, b evItem) int { return a.kind - b.kind }) // the recordings, then the screenshots
		parts := append([]string{"<details><summary>Recordings and every screenshot</summary>"}, vids...)
		for _, it := range more {
			parts = append(parts, it.caption+"\n"+it.image())
		}
		add("%s\n\n</details>", strings.Join(parts, "\n\n"))
	}
	limits := slices.Clone(ev.limits)
	for _, r := range v.Reasons { // a reason that opens with a check's id and name is that check's note, already told above
		switch {
		case v.Overall == CheckBlocked:
			limits = append(limits, "I could not run the checks. "+recase(plainSentence(r, 300), true))
		case r == reasonNoneReached && count(v.Checks).unreached > 0: // every check was skipped: the result line says so
		case !slices.ContainsFunc(v.Checks, func(c Check) bool { return strings.HasPrefix(r, c.ID+" "+c.Name+": ") }):
			limits = append(limits, plainReason(r, main))
		}
	}
	for i, l := range limits {
		if i == f.bullets {
			limits = append(limits[:i], fmt.Sprintf("And %d more.", len(limits)-i))
			break
		}
		limits[i] = clip(l, f.limit)
	}
	if len(limits) > 0 {
		add("%s", strings.Join(limits, "\n"))
	}
	add("%s", took(now.Sub(run.CreatedAt)))
	return strings.Join(blocks, "\n\n")
}

// videoParts are the details block's entries for the uploaded videos, ahead of the GIFs: a caption, then the link in
// a paragraph of its own so GitHub shows a player. A video that did not go up says so, and points at the GIF when
// there is one.
func videoParts(run Run, ev evidence) (out []string) {
	for _, v := range ev.videos {
		side := sideLabel(run, v.leg)
		if v.url == "" {
			note := side + " video could not be attached"
			if slices.ContainsFunc(ev.items, func(it evItem) bool { return it.kind == kindGIF && it.file == sideSlug(run, v.leg)+".gif" }) {
				note += "; the GIF below shows the steps"
			}
			out = append(out, note+".")
			continue
		}
		how := map[bool]string{true: "only the moments around each step"}[v.cut]
		if v.stitched {
			how = "stitched from the step screenshots because the live recording was lost"
		}
		caption := slices.DeleteFunc([]string{side, spanText(v.span), how}, func(s string) bool { return s == "" })
		out = append(out, strings.Join(caption, ", ")+"\n\n"+v.url)
	}
	return out
}

var (
	words     = regexp.MustCompile(`[a-z0-9]+`)
	nameWords = regexp.MustCompile(`[A-Z]+[a-z0-9]*|[a-z0-9]+`) // TitlebarNav is titlebar and nav
)

// bareStems are file-name words too common to show that a check was about the file.
var bareStems = []string{"index", "main", "app", "use", "get", "set", "util", "utils", "type", "types", "test", "spec", "project", "file", "files"}

// covers reports whether a check's words share a stem with a word of one of the area's file names: nav is the stem
// of navigate. A run from before areas kept file names falls back to the words of the area's own name.
func covers(said []string, a Area) bool {
	stems := a.Stems
	if len(stems) == 0 {
		stems = []string{a.Name}
	}
	for _, stem := range stems {
		for _, w := range nameWords.FindAllString(stem, -1) {
			if w = strings.ToLower(w); len(w) >= 3 && !slices.Contains(bareStems, w) && slices.ContainsFunc(said, func(t string) bool { return strings.HasPrefix(t, w) }) {
				return true
			}
		}
	}
	return false
}

// areaLine says which changed areas (six at most) the checks touched. Tests are never run here. Another area is
// covered when a check shares a word stem with one of its files, or when it is the only one: the checks came from the
// PR's own claims, so they are about it. A single file is named: "the renderer component TitlebarNav".
func areaLine(areas []Area, checks []Check) string {
	if len(areas) == 0 {
		return ""
	}
	var said []string
	for _, c := range checks {
		said = append(said, words.FindAllString(strings.ToLower(c.Name+" "+c.Expect+" "+c.Note), -1)...)
	}
	others := len(areas)
	if slices.ContainsFunc(areas, func(a Area) bool { return a.Name == "Tests" }) {
		others--
	}
	out := "What the PR touches."
	for i, a := range areas {
		if i == 6 {
			return out + fmt.Sprintf(" And %d more %s.", len(areas)-i, map[bool]string{true: "area", false: "areas"}[len(areas)-i == 1])
		}
		status, who := "not run here", fmt.Sprintf("%s, %d %s", mdText(a.Name, 40), a.Files, map[bool]string{true: "file", false: "files"}[a.Files == 1])
		if a.Name != "Tests" {
			status = "not touched by this run"
			if others == 1 || covers(said, a) {
				status = "covered above"
			}
			if a.Files == 1 && len(a.Stems) == 1 {
				who = "The " + singular(lowerFirst(mdText(a.Name, 40))) + " " + mdText(a.Stems[0], 40)
			}
		}
		out += " " + who + ", " + status + "."
	}
	return out
}

// singular drops the plural s of a name's last word: components is component, but status stays.
func singular(s string) string {
	if strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && !strings.HasSuffix(s, "us") && !strings.HasSuffix(s, "is") {
		return s[:len(s)-1]
	}
	return s
}

// plainCell is a tester's note without its "Base:" prefix, clipped and made safe: the table's note column.
func plainCell(note string, n int) string {
	return mdText(legPrefix.ReplaceAllString(strings.TrimSpace(note), ""), n)
}

// postComment writes comment.md (the exact body) and, when the run asked for it and is not blocked, publishes the
// evidence images and posts it. It never fails the submit: the outcome is the comment's URL and a state (posted,
// updated, off or failed:<reason>), plus what became of the evidence. Evidence is published only once the token's
// login is known, so a gh that cannot post does not push files either.
func (s *Service) postComment(ctx context.Context, run Run, v Verdict, videos []legVideo) (url, state string, ev evidence) {
	ev = evidence{state: EvidenceOff, videoState: "off"}
	write := func() (string, error) {
		body := renderComment(run, v, ev, s.clock())
		return body, s.store.WriteFile(run.ID, "comment.md", []byte(body))
	}
	body, err := write()
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	if run.Comment != CommentPost || v.Overall == CheckBlocked {
		return "", CommentOff, ev
	}
	lctx, cancel := context.WithTimeout(ctx, commentTimeout)
	_, err = s.ghLogin(lctx)
	cancel()
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	if testsOnly(v.Checks) { // no window was driven: a recording of an idle app shows nothing
		videos = nil
	}
	ev = s.publishEvidence(ctx, run, v)
	ev.videos, ev.videoState = s.publishVideos(ctx, run, videos) // after the images, and without them when they failed
	if len(ev.items) > 0 || len(ev.limits) > 0 || len(ev.videos) > 0 {
		if body, err = write(); err != nil {
			return "", s.commentFailed(run, err), ev
		}
	}
	pctx, cancel := context.WithTimeout(ctx, commentTimeout)
	defer cancel()
	url, state, err = s.upsertComment(pctx, run, v.Overall, body)
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	return url, state, ev
}

// commentFailed logs err to comment.log in the run dir and returns the state it earns.
func (s *Service) commentFailed(run Run, err error) string {
	slog.Warn("litmus: PR comment failed", "run", run.ID, "error", err)
	s.commentLog(run, err)
	reason, _, _ := strings.Cut(err.Error(), "\n")
	return "failed:" + clip(strings.TrimSpace(reason), 120)
}

func (s *Service) commentLog(run Run, err error) { s.commentLogf(run, "%v", err) }

func (s *Service) commentLogf(run Run, format string, a ...any) {
	f, ferr := os.OpenFile(filepath.Join(s.store.root, run.ID, "comment.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %s\n", s.clock().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// upsertComment edits this PR's Litmus comment written by the token's login, or posts one. A failing verdict also
// asks for changes in a review, once per call: a review body cannot be edited.
func (s *Service) upsertComment(ctx context.Context, run Run, overall CheckState, body string) (string, string, error) {
	login, err := s.ghLogin(ctx)
	if err != nil {
		return "", "", err
	}
	n, api := strconv.Itoa(run.PRNumber), "repos/"+run.Repo
	raw, err := s.gh.Run(ctx, "api", api+"/issues/"+n+"/comments", "--paginate")
	if err != nil {
		return "", "", err
	}
	id, err := findComment(raw, commentMarker(run.Repo, run.PRNumber), login)
	if err != nil {
		return "", "", fmt.Errorf("unreadable comment list: %w", err)
	}
	method, path, state := "POST", api+"/issues/"+n+"/comments", "posted"
	if id != 0 {
		method, path, state = "PATCH", api+"/issues/comments/"+strconv.FormatInt(id, 10), "updated"
	}
	out, err := s.ghSend(ctx, run, method, path, map[string]string{"body": body})
	if err != nil {
		return "", "", err
	}
	var sent struct {
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(out, &sent)
	if overall == CheckFail {
		if err := s.requestChanges(ctx, run, login, sent.HTMLURL); err != nil { // the comment is up; only the review failed
			s.commentLog(run, fmt.Errorf("review not submitted: %w", err))
		}
	}
	return sent.HTMLURL, state, nil
}

// requestChanges submits the REQUEST_CHANGES review, unless the token owns the PR: GitHub refuses that from an author.
func (s *Service) requestChanges(ctx context.Context, run Run, login, commentURL string) error {
	n := strconv.Itoa(run.PRNumber)
	author, err := s.gh.Run(ctx, "pr", "view", n, "--repo", run.Repo, "--json", "author", "--jq", ".author.login")
	if err != nil || strings.EqualFold(strings.TrimSpace(string(author)), login) {
		return err
	}
	link := cmp.Or(commentURL, fmt.Sprintf("https://github.com/%s/pull/%d", run.Repo, run.PRNumber))
	_, err = s.ghSend(ctx, run, "POST", "repos/"+run.Repo+"/pulls/"+n+"/reviews",
		map[string]string{"event": "REQUEST_CHANGES", "body": "Litmus found a failing check on this pull request.\nDetails: " + link})
	return err
}

// ghLogin is the login the gh token acts as, asked once per service.
func (s *Service) ghLogin(ctx context.Context) (string, error) {
	s.mu.Lock()
	login := s.login
	s.mu.Unlock()
	if login != "" {
		return login, nil
	}
	out, err := s.gh.Run(ctx, "api", "user", "--jq", ".login")
	if login = strings.TrimSpace(string(out)); err == nil && login == "" {
		err = errors.New("gh api user returned no login")
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.login = login
	s.mu.Unlock()
	return login, nil
}

// ghSend calls the GitHub API with a JSON body read from a file, never from argv, and returns the object gh prints.
func (s *Service) ghSend(ctx context.Context, run Run, method, path string, body map[string]string) ([]byte, error) {
	data, _ := json.Marshal(body)
	f, err := os.CreateTemp(filepath.Join(s.store.root, run.ID), ".gh-*.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return s.gh.Run(ctx, "api", "--method", method, path, "--input", f.Name())
}

// findComment returns the id of the comment holding marker that login wrote, or 0. `gh --paginate` prints one JSON
// array per page, back to back.
func findComment(raw []byte, marker, login string) (int64, error) {
	for dec := json.NewDecoder(bytes.NewReader(raw)); dec.More(); {
		var page []struct {
			ID   int64
			Body string
			User struct{ Login string }
		}
		if err := dec.Decode(&page); err != nil {
			return 0, err
		}
		for _, c := range page {
			if strings.Contains(c.Body, marker) && strings.EqualFold(c.User.Login, login) {
				return c.ID, nil
			}
		}
	}
	return 0, nil
}
