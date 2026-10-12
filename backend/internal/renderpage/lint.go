package renderpage

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Guide is the design contract for every page an agent writes: a render and
// an HTML artifact alike. The system prompt and the html tools carry it, and
// Lint enforces it.
//
//go:embed guide.md
var Guide string

//go:embed kit.css
var kitCSS string

// Finding is one place a page breaks Guide.
type Finding struct {
	Line    int
	Rule    string
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("line %d: %s (%s)", f.Line, f.Message, f.Rule)
}

// FindingsError formats findings as the error an agent reads: every finding,
// then how to fix them.
func FindingsError(findings []Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the page breaks the AO page design rules in %d place(s):\n", len(findings))
	for _, f := range findings {
		b.WriteString("  " + f.String() + "\n")
	}
	b.WriteString("Fix every finding. Use only the elements and ao- classes in the AO page design rules; AO supplies all colors, fonts, and spacing from the live theme.")
	return b.String()
}

var (
	// kitClasses is every class the kit defines, the whole class vocabulary.
	kitClasses = func() []string {
		var out []string
		for _, m := range regexp.MustCompile(`\.(ao-[a-z0-9-]+)`).FindAllStringSubmatch(kitCSS, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
		return out
	}()
	allowedData = map[string][]string{
		"data-tone":    {"neutral", "info", "success", "warning", "danger"},
		"data-cols":    {"3"},
		"data-variant": {"primary"},
	}
	bannedElements = []string{"style", "link", "canvas", "font", "center", "iframe", "object", "embed", "marquee", "basefont"}
	// Attributes that paint: SVG presentation colors and legacy HTML colors.
	paintAttributes = []string{"fill", "stroke", "stop-color", "flood-color", "lighting-color", "color", "bgcolor", "text", "link", "vlink", "alink"}

	lintScript   = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	lintComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	lintTag      = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9:-]*)((?:\s+[^\s=>/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>"']+))?)*)\s*/?>`)
	lintAttr     = regexp.MustCompile(`([^\s=>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+)))?`)
	lintScriptJS = regexp.MustCompile(`(?i)^\s*$|javascript|module|ecmascript`)
	styleValue   = regexp.MustCompile(`^\s*--value\s*:\s*-?\d+(?:\.\d+)?\s*;?\s*$`)
	scriptStyle  = regexp.MustCompile(`\.style\b|cssText|setAttribute\(\s*["'\x60]style|insertRule|adoptedStyleSheets|createElement\(\s*["'\x60](?:style|link)`)
	colorFunc    = regexp.MustCompile(`(?i)\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color-mix|color)\(`)
	quotedHex    = regexp.MustCompile(`["'\x60]#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})["'\x60]`)
	textRun      = regexp.MustCompile(`>([^<]+)<`)
	emoji        = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}]`)
	headingText  = regexp.MustCompile(`(?is)<(h[123]|th)\b[^>]*>(.*?)</(?:h[123]|th)\s*>`)
	innerTag     = regexp.MustCompile(`<[^>]*>`)
	// Code-like text keeps its own case.
	codeSpans = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<code\b.*?</code\s*>`),
		regexp.MustCompile(`(?is)<pre\b.*?</pre\s*>`),
		regexp.MustCompile(`(?is)<kbd\b.*?</kbd\s*>`),
		regexp.MustCompile(`(?is)<samp\b.*?</samp\s*>`),
	}
)

// Lint reports every place page breaks Guide, in page order. It reads the
// markup with patterns rather than an HTML parser, which this module does
// not depend on; it is meant for the pages agents write, not arbitrary HTML.
func Lint(page string) []Finding {
	var findings []Finding
	add := func(at int, rule, format string, args ...any) {
		findings = append(findings, Finding{Line: 1 + strings.Count(page[:at], "\n"), Rule: rule, Message: fmt.Sprintf(format, args...)})
	}
	// Scripts and comments are checked, then blanked with their newlines kept,
	// so markup inside them is not read as the page's and lines still match.
	masked := []byte(page)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if masked[i] != '\n' {
				masked[i] = ' '
			}
		}
	}
	for _, m := range lintScript.FindAllStringSubmatchIndex(page, -1) {
		attrs, body := page[m[2]:m[3]], page[m[4]:m[5]]
		if src, ok := attrValue(attrs, "src"); ok && strings.TrimSpace(src) != "" {
			add(m[0], "script-src", "<script src> loads code from outside the page; inline the script, and do not use libraries")
		}
		typ, _ := attrValue(attrs, "type")
		if lintScriptJS.MatchString(typ) {
			if loc := scriptStyle.FindStringIndex(body); loc != nil {
				add(m[4]+loc[0], "script-style", "the script sets styles (%q); build markup with ao- classes instead", body[loc[0]:loc[1]])
			}
			if loc := colorFunc.FindStringIndex(body); loc != nil {
				add(m[4]+loc[0], "color", "the script writes a color (%q); use an ao-series-N class", body[loc[0]:loc[1]])
			} else if loc := quotedHex.FindStringIndex(body); loc != nil {
				add(m[4]+loc[0], "color", "the script writes a color (%s); use an ao-series-N class", body[loc[0]:loc[1]])
			}
		}
		blank(m[4], m[5])
	}
	for _, m := range lintComment.FindAllStringIndex(page, -1) {
		blank(m[0], m[1])
	}
	markup := string(masked)

	roots := 0
	h1s := 0
	lede := false
	for _, m := range lintTag.FindAllStringSubmatchIndex(markup, -1) {
		at := m[0]
		name := strings.ToLower(markup[m[2]:m[3]])
		attrs := markup[m[4]:m[5]]
		if slices.Contains(bannedElements, name) {
			add(at, "element", "<%s> is not allowed; AO supplies all styling, fonts, and charts", name)
			continue
		}
		if name == "h1" {
			h1s++
		}
		for _, a := range lintAttr.FindAllStringSubmatch(attrs, -1) {
			attr := strings.ToLower(a[1])
			value := a[2] + a[3] + a[4]
			switch {
			case attr == "class":
				for _, class := range strings.Fields(value) {
					if !slices.Contains(kitClasses, class) {
						add(at, "class", "class %q is not an AO class; use only the ao- classes in the AO page design rules", class)
					}
				}
				classes := strings.Fields(value)
				if name == "main" && slices.Contains(classes, "ao-page") {
					roots++
				}
				if slices.Contains(classes, "ao-lede") {
					lede = true
				}
			case attr == "style":
				if name != "li" || !styleValue.MatchString(value) {
					add(at, "style-attribute", "style=%q is not allowed; the only style attribute is style=\"--value:N\" on an .ao-bars li", value)
				}
			case strings.HasPrefix(attr, "data-"):
				allowed, ok := allowedData[attr]
				if !ok {
					add(at, "data-attribute", "%s is not an AO attribute; use data-tone, data-cols, or data-variant", attr)
				} else if !slices.Contains(allowed, value) {
					add(at, "data-attribute", "%s=%q is not allowed; use one of %s", attr, value, strings.Join(allowed, ", "))
				}
			case slices.Contains(paintAttributes, attr):
				if v := strings.ToLower(strings.TrimSpace(value)); (attr != "fill" && attr != "stroke") || (v != "none" && v != "currentcolor") {
					add(at, "color", "%s=%q sets a color; use fill or stroke \"none\" or \"currentColor\" with an ao-series-N class", attr, value)
				}
			}
		}
	}
	if roots != 1 {
		add(0, "page-root", "the page needs exactly one <main class=\"ao-page\">, found %d; start from the skeleton in the AO page design rules", roots)
	}
	if h1s != 1 {
		add(0, "heading", "the page needs exactly one <h1>, in its header; found %d", h1s)
	}
	if !lede {
		add(0, "lede", "the header needs a <p class=\"ao-lede\"> that states the conclusion")
	}

	for _, re := range codeSpans {
		for _, m := range re.FindAllStringIndex(markup, -1) {
			blank(m[0], m[1])
		}
	}
	markup = string(masked)
	for _, m := range textRun.FindAllStringSubmatchIndex(markup, -1) {
		text := markup[m[2]:m[3]]
		if loc := emoji.FindStringIndex(text); loc != nil {
			add(m[2]+loc[0], "emoji", "emoji and pictographs (%q) are not allowed; use words or an ao-status", text[loc[0]:loc[1]])
		}
		if shouting(text) {
			add(m[2], "case", "%q is ALL CAPS; write it in sentence case", strings.TrimSpace(text))
		}
	}
	for _, m := range headingText.FindAllStringSubmatchIndex(markup, -1) {
		text := innerTag.ReplaceAllString(markup[m[4]:m[5]], " ")
		if titleCase(text) {
			add(m[0], "case", "%q is in title case; write headings and labels in sentence case", strings.Join(strings.Fields(text), " "))
		}
	}
	slices.SortStableFunc(findings, func(a, b Finding) int { return a.Line - b.Line })
	return findings
}

// attrValue is the value of attribute name in a tag's attribute text.
func attrValue(attrs, name string) (string, bool) {
	for _, a := range lintAttr.FindAllStringSubmatch(attrs, -1) {
		if strings.EqualFold(a[1], name) {
			return a[2] + a[3] + a[4], true
		}
	}
	return "", false
}

// shouting reports text with no lowercase letter and two or more words of
// four capitals or more: "TOTAL COST", but not "API" or "CPU / GPU".
func shouting(text string) bool {
	words := 0
	for _, word := range strings.Fields(text) {
		n := 0
		for _, r := range word {
			if unicode.IsLower(r) {
				return false
			}
			if unicode.IsUpper(r) {
				n++
			}
		}
		if n >= 4 {
			words++
		}
	}
	return words >= 2
}

// titleCase reports a heading whose words after the first, at least two of
// them four letters or longer, are all capitalized ordinary words: "Weekly
// Revenue Report", but not "Revenue by region" or "Runs on GitHub Actions"
// (a mixed-case name like GitHub is skipped, not counted).
func titleCase(text string) bool {
	words := strings.Fields(text)
	long := 0
	for _, word := range words[min(1, len(words)):] {
		runes := []rune(strings.TrimFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }))
		if len(runes) < 4 {
			continue
		}
		if !unicode.IsUpper(runes[0]) {
			return false
		}
		if strings.IndexFunc(string(runes[1:]), unicode.IsUpper) >= 0 {
			continue
		}
		long++
	}
	return long >= 2
}
