## AO page design rules

These rules apply to every HTML page you write: a page shown in a chat thread, and an HTML artifact you save and attach with `ao report --artifact`. They are mandatory. AO lints every page: it refuses to show a page that breaks a rule, and refuses `ao report --artifact` for an HTML file that breaks one. Run `ao render --lint <file>` and fix every finding before you show or attach the page.

AO styles the page, not you. When AO serves the page it adds its stylesheet and the reader's live app theme, so the page follows theme and light/dark changes as they happen. Your page is semantic HTML plus the AO classes below. Nothing else.

### Skeleton

Every page has exactly this structure. Start from it; do not add wrappers around `<main>`.

```html
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Sentence-case title</title>
<main class="ao-page">
  <header>
    <h1>Sentence-case title</h1>
    <p class="ao-lede">One sentence that states the conclusion.</p>
    <p class="ao-meta">Source or scope · date</p>
  </header>
  <section>
    <h2>Section title</h2>
    <!-- content: elements and classes below only -->
  </section>
</main>
```

- One `<h1>`, in the header. `ao-lede` is required; `ao-meta` is optional.
- Content goes in `<section>` elements, each starting with an `<h2>`. Use `<h3>` only inside a section.
- Lead with the answer. The first section holds the most important finding.

### Elements

Use plain elements; AO styles them: `h1` `h2` `h3` `p` `a` `strong` `em` `code` `pre` `kbd` `ul` `ol` `li` `dl` `dt` `dd` `table` `thead` `tbody` `tr` `th` `td` `figure` `figcaption` `blockquote` `details` `summary` `hr` `time` `img` `svg` and SVG shapes.

### Classes

These are the only classes a page may use. Any other class is an error.

| Need | Markup |
| --- | --- |
| Page root | `<main class="ao-page">` (required, exactly once) |
| Summary sentence under the title | `<p class="ao-lede">` |
| Secondary text: sources, dates, notes | `class="ao-meta"` |
| 2-4 headline numbers | `<div class="ao-stats">` with `<div class="ao-stat">` children, each holding `<span class="ao-stat-label">`, `<span class="ao-stat-value">`, optional `<span class="ao-stat-note">` |
| Table | `<div class="ao-table"><table>…</table></div>`; numeric `th` and `td` get `class="ao-num"` |
| List of peer items | `<ul class="ao-list">`, each `<li>` holding `<span>` with the item text (it may start with a `<strong>` name), then optionally one `ao-meta` or `ao-status` |
| Dated sequence, plan, history | `<ol class="ao-timeline">`, each `<li>` holding `<time>` then the text |
| State of an item | `<span class="ao-status" data-tone="…">Sentence-case label</span>` |
| A finding the reader must not miss | `<p class="ao-callout" data-tone="…">` (at most 2 per page) |
| Horizontal bar chart (ranked or compared values) | `<ol class="ao-bars">`, each `<li style="--value:62">` holding `<span class="ao-bar-label">` then `<span class="ao-bar-value">`; `--value` is the bar length, 0-100, as a percentage of the largest value |
| Any other chart | `<figure class="ao-chart">` with an inline `<svg viewBox="0 0 640 240">`, then `<ul class="ao-legend">` when there is more than one series, then `<figcaption>` |
| Chart series color | `class="ao-series-1"` … `ao-series-6` on an SVG shape, a `.ao-bars` `li`, or a `.ao-legend` `li`; use them in order |
| SVG line, area, axis, grid line, value label | `ao-line`, `ao-area`, `ao-axis`, `ao-gridline`, `ao-value` |
| Side-by-side blocks | `<div class="ao-grid">` (2 columns) or `<div class="ao-grid" data-cols="3">` |
| Grouped block, UI mockup panel | `<div class="ao-surface">` |
| Mockup layout | `ao-row` (horizontal), `ao-stack` (vertical), `ao-spacer` (pushes the rest to the end) |
| Mockup controls | `<span class="ao-button">` (add `data-variant="primary"` for the one primary action), `<span class="ao-input">` |

`data-tone` is `neutral`, `info`, `success`, `warning`, or `danger`, and means what it says: `success` only for done or passing, `warning` for needs attention, `danger` for failed or blocking, `info` for in progress. Never use a tone for decoration.

### Theme tokens

AO injects the app's live theme as CSS custom properties on `:root`. The kit above is built from them, so you never write them yourself:

`--background` `--foreground` `--muted` `--muted-foreground` `--card` `--card-foreground` `--popover` `--border` `--border-strong` `--primary` `--primary-foreground` `--accent` `--accent-foreground` `--success` `--warning` `--destructive` `--code` `--link` `--chart-1` `--chart-2` `--chart-3` `--chart-4` `--chart-5` `--chart-6` `--radius` `--font-sans` `--font-mono`

Their values change when the reader changes the theme or switches light and dark, and the page updates in place. A page that writes its own colors, fonts, or sizes does not follow the theme and is refused.

### Never

- No `<style>` element, no `<link rel="stylesheet">`, no `@import`, no web fonts. The only `style` attribute allowed is `style="--value:N"` on an `.ao-bars` `li`.
- No color values anywhere in markup or script: no hex, `rgb()`, `hsl()`, `oklch()`, or named colors. SVG `fill` and `stroke` attributes may only be `none` or `currentColor`; color an SVG shape with an `ao-series-N` class.
- No JavaScript that sets styles (`.style`, `cssText`, `setAttribute("style")`). No `<canvas>`, no chart or UI libraries, no CSS frameworks, no `<script src>`. An inline `<script>` may only build kit markup from data or add interaction such as sorting.
- No classes or `data-` attributes other than those above. No `id` used for styling.
- No emoji, icon fonts, or decorative images. No gradients, shadows, glows, or animations.
- No ALL CAPS text and no title case: sentence case everywhere, including headings, table headers, labels, and chart text.
- No cards inside cards, no banner or hero header, no logo, no footer, no "generated by" line.
- No fixed page width and no outer padding: AO sizes and pads the page for where it is shown.

### Content

- Write for an engineer scanning between tasks: short sentences, concrete numbers, units on every number.
- One idea per section. Prefer a table to a run of stats, and one chart to several small ones.
- Charts: label axes in `<text>` inside the SVG, start bar axes at zero, at most 6 series, one `<figcaption>` saying what the chart shows.
- Keep it as short as the content allows. No introduction that restates the title, no closing summary that repeats the sections.
