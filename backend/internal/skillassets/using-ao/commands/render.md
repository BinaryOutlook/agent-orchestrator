# ao render

Show a self-contained HTML page inline in the current **chat** session's
thread, above your final reply. Use it when a chart, table, diagram, image
collage, or mockup says more than prose.

In a chat session, the `html_preview` and `html_render` tools do the same as
`ao render --check` and `ao render`. They work without shell access to the
daemon, so use them when you have them. The rules below apply to both.

```bash
ao render "$TMPDIR/turns-by-day.html" --title "Turns by day" --height 420
```

## Rules

- Call it before your final reply. The reader already sees the page, so the
  reply must not announce it, say where it is, or restate it. Add only what the
  page does not say.
- Follow the AO page design rules: `ao render --guide` prints them, and every
  session's system prompt carries them. The page is semantic HTML and AO's `ao-`
  classes, with no styles, colors, fonts, or libraries of its own. AO refuses a
  page that breaks a rule; `ao render --lint <file>` lists every finding.
- One self-contained HTML file, at most 1 MiB. Relative URLs do not resolve.
  Web workers do not run, and a page can reach no address on this computer or
  its local network.
- To show a local image, write its absolute path: `src="/abs/shot.png"` or a JS
  string. `ao render` puts the image into the page. PNG, JPEG, GIF, WebP, AVIF,
  SVG, BMP, and ICO files work, each up to 10 MiB. Remote http(s) image URLs load
  as they are.
- Write the file outside the repository (for example under `$TMPDIR`) so it does
  not appear in the diff. AO stores its own copy.
- AO measures the page when you publish it, so the frame opens at the right
  height. `--height` (80-2000) is used only when the desktop app is not running.
  The frame then fits the page's real height.
- Chat sessions only. In a terminal session, open the file with `ao preview`.
- Do not use a built-in visualize skill, for example the Codex visualize skill, or a
  `visualize{...}` line. AO does not show them. Only `ao render` or the `html_render` tool shows a page in the thread.

## Check before you publish

Run `ao render --check <file>` first. It loads the page in the AO desktop app
the way readers see it and writes a PNG. It prints the page's content height,
its console messages, and its design findings. Read the PNG. Fix every
`console.error` line and every design finding. Use
`--width 390` to check a phone layout. The check loads only public addresses. A
request to this computer or to your local network fails. The check needs the
desktop app. Without it, publish without a check.

## ao render or the Browser panel

Use `ao render` when the page is the answer: a chart, table, diagram, or
mockup that you make from data you already have. The page stays in the thread.

Use `ao preview` and `ao browser` when the page is the work: an app that runs,
a page that needs a server or a login, or a page that you must click through.
The Browser panel shows the live page, and the next preview replaces it.

If the user must still read the page after the dev server stops, use `ao render`.

## ao render or a session artifact

Use `ao render` (or `html_render`) when the page answers a question in the thread.
Write a session artifact when the user asks for something to keep, for example a
report, a dashboard, or a document. Write it to the artifact directory that your
prompt names, run `ao render --lint <path>`, and attach it with
`ao report --artifact <path>`. An HTML artifact follows the same design rules as
a rendered page, and `ao report` refuses one that breaks them.
In a chat session, an HTML file from the artifact directory that you attach
with `--artifact` also shows in the thread.

To keep a rendered page as an artifact as well, add `--artifact` to `ao render`
(or set `artifact: true` in `html_render`). Do this only when the user asks to keep
the page.

## Layout and theme

- The frame is borderless on the thread background, as wide as the reply column,
  and its left edge lines up with your text. When the reader expands the page,
  it opens in a dialog up to 1024 px wide that fits its height.
- AO sizes and pads `.ao-page` for where it shows: inline in the thread,
  expanded, or on its own in the Browser panel. Do not set a width or padding.
- AO injects the app's live theme as CSS custom properties on `:root`, and its
  page kit is built from them, so the page follows theme and light/dark changes
  as they happen. The design rules list the variables; you never write them.
- Scripts run in a sandbox with no access to AO, cookies, or storage. Links open
  in the user's browser.
