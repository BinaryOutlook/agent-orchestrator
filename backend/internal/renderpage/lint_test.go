package renderpage

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

const goodPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Test runtime by package</title>
<main class="ao-page">
  <header>
    <h1>Test runtime by package</h1>
    <p class="ao-lede">The session package takes 61% of the suite's 4m 12s.</p>
    <p class="ao-meta">go test ./... on main · 12 Oct 2026</p>
  </header>
  <section>
    <h2>Headline numbers</h2>
    <div class="ao-stats">
      <div class="ao-stat"><span class="ao-stat-label">Total</span><span class="ao-stat-value">4m 12s</span></div>
      <div class="ao-stat"><span class="ao-stat-label">Packages</span><span class="ao-stat-value">84</span><span class="ao-stat-note">3 over 30s</span></div>
    </div>
    <p class="ao-callout" data-tone="warning">The race build doubles session_manager time.</p>
  </section>
  <section>
    <h2>Slowest packages on GitHub Actions</h2>
    <ol class="ao-bars">
      <li style="--value:100"><span class="ao-bar-label">session_manager</span><span class="ao-bar-value">153s</span></li>
      <li class="ao-series-2" style="--value:31.5"><span class="ao-bar-label">httpd</span><span class="ao-bar-value">48s</span></li>
    </ol>
    <figure class="ao-chart">
      <svg viewBox="0 0 640 240" role="img" aria-label="Runtime per day">
        <line class="ao-axis" x1="40" y1="200" x2="620" y2="200"/>
        <path class="ao-line ao-series-1" d="M40 180 L620 60" fill="none" stroke="currentColor"/>
        <text x="40" y="220">Mon</text>
      </svg>
      <ul class="ao-legend"><li class="ao-series-1">Wall time</li></ul>
      <figcaption>Suite wall time per day, seconds.</figcaption>
    </figure>
    <div class="ao-table"><table>
      <thead><tr><th>Package</th><th class="ao-num">Time</th><th>State</th></tr></thead>
      <tbody><tr><td><code>HTTP_TIMEOUT_MS DEFAULT_PORT</code></td><td class="ao-num">153s</td><td><span class="ao-status" data-tone="danger">Over budget</span></td></tr></tbody>
    </table></div>
    <ul class="ao-list"><li>CPU / GPU split<span class="ao-meta">API only</span></li></ul>
  </section>
</main>
<script>
const rows = document.querySelectorAll(".ao-bars li");
rows.forEach((row) => row.classList.add("ao-series-1"));
</script>
`

func rules(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Rule)
	}
	return out
}

func TestLintPassesAPageThatFollowsTheGuide(t *testing.T) {
	if findings := Lint(goodPage); len(findings) != 0 {
		t.Fatalf("findings = %v", findings)
	}
}

func TestLintReportsEachBrokenRule(t *testing.T) {
	cases := map[string]struct {
		from, to string
		rule     string
	}{
		"style element":         {"<main", "<style>body{color:red}</style><main", "element"},
		"stylesheet link":       {"<main", `<link rel="stylesheet" href="https://x/y.css"><main`, "element"},
		"canvas":                {"</section>\n</main>", "<canvas></canvas></section>\n</main>", "element"},
		"foreign class":         {`class="ao-stats"`, `class="ao-stats card"`, "class"},
		"style attribute":       {`<p class="ao-callout"`, `<p style="color:#f00" class="ao-callout"`, "style-attribute"},
		"value on a non-li":     {`<p class="ao-callout"`, `<p style="--value:3" class="ao-callout"`, "style-attribute"},
		"unknown data attr":     {`data-tone="warning"`, `data-tone="warning" data-color="x"`, "data-attribute"},
		"unknown tone":          {`data-tone="warning"`, `data-tone="brand"`, "data-attribute"},
		"svg fill color":        {`fill="none" stroke="currentColor"`, `fill="#79b0dc"`, "color"},
		"legacy bgcolor":        {"<table>", `<table bgcolor="black">`, "color"},
		"script src":            {"<script>", `<script src="https://cdn.jsdelivr.net/npm/chart.js"></script><script>`, "script-src"},
		"script sets style":     {`row.classList.add("ao-series-1")`, `row.style.color = "red"`, "script-style"},
		"script hex color":      {`row.classList.add("ao-series-1")`, `row.dataset.c = "#ff0000"`, "color"},
		"script color function": {`row.classList.add("ao-series-1")`, `row.dataset.c = "rgb(1,2,3)"`, "color"},
		"no root":               {`<main class="ao-page">`, `<main>`, "page-root"},
		"two h1":                {"<h2>Headline numbers</h2>", "<h1>Headline numbers</h1>", "heading"},
		"no lede":               {`class="ao-lede"`, `class="ao-meta"`, "lede"},
		"emoji":                 {"Over budget", "Over budget 🚨", "emoji"},
		"all caps":              {"<h2>Headline numbers</h2>", "<h2>HEADLINE NUMBERS</h2>", "case"},
		"title case":            {"<h2>Headline numbers</h2>", "<h2>Key Headline Numbers</h2>", "case"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(goodPage, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			got := rules(Lint(strings.Replace(goodPage, tc.from, tc.to, 1)))
			if !slices.Contains(got, tc.rule) {
				t.Fatalf("rules = %v, want %s", got, tc.rule)
			}
		})
	}
}

func TestLintReportsTheLine(t *testing.T) {
	page := strings.Replace(goodPage, `<p class="ao-callout"`, `<p class="ao-callout fancy"`, 1)
	findings := Lint(page)
	if len(findings) != 1 || findings[0].Line != 17 {
		t.Fatalf("findings = %v, want one on line 17", findings)
	}
}

func TestLintIgnoresMarkupInsideScriptsAndComments(t *testing.T) {
	page := strings.Replace(goodPage, "</main>", `<!-- <div class="card"></div> --></main>`, 1)
	page = strings.Replace(page, "const rows", "const tpl = '<div class=\"card\"></div>';\nconst rows", 1)
	if findings := Lint(page); len(findings) != 0 {
		t.Fatalf("findings = %v", findings)
	}
}

// The guide is the contract agents read: it names every class the kit defines
// and every theme variable AO injects, and nothing else.
func TestGuideMatchesTheKitAndTheTheme(t *testing.T) {
	documented := regexp.MustCompile(`\bao-[a-z0-9-]*[a-z0-9]`).FindAllString(Guide, -1)
	// The guide writes the series as "ao-series-1 … ao-series-6" and "ao-series-N".
	documented = slices.DeleteFunc(documented, func(class string) bool { return class == "ao-series" })
	documented = append(documented, "ao-series-2", "ao-series-3", "ao-series-4", "ao-series-5")
	for _, class := range kitClasses {
		if !slices.Contains(documented, class) {
			t.Errorf("kit class %s is not in the guide", class)
		}
	}
	for _, class := range documented {
		if !slices.Contains(kitClasses, class) {
			t.Errorf("guide names %s, which the kit does not define", class)
		}
	}
	tokens := regexp.MustCompile(`--[a-z0-9-]+`).FindAllString(strings.SplitN(defaultThemeCSS, "@media", 2)[0], -1)
	section := Guide[strings.Index(Guide, "### Theme tokens"):strings.Index(Guide, "### Never")]
	var listed []string
	for _, m := range regexp.MustCompile("`(--[a-z0-9-]+)`").FindAllStringSubmatch(section, -1) {
		listed = append(listed, m[1])
	}
	if !slices.Equal(listed, tokens) {
		t.Fatalf("guide tokens = %v\ntheme tokens = %v", listed, tokens)
	}
}
