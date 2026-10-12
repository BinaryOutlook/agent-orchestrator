package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/renderpage"
)

const (
	maxRenderFileBytes  = 1 << 20
	defaultRenderHeight = 400
	defaultRenderWidth  = 720
)

type renderAPIRequest struct {
	HTML     string `json:"html"`
	Title    string `json:"title"`
	Height   int    `json:"height"`
	Artifact bool   `json:"artifact,omitempty"`
}

type renderAPIResponse struct {
	RenderID      string `json:"renderId"`
	ActivityID    string `json:"activityId"`
	Path          string `json:"path"`
	ArtifactPath  string `json:"artifactPath"`
	ArtifactError string `json:"artifactError"`
}

type renderCheckAPIRequest struct {
	HTML  string `json:"html"`
	Width int    `json:"width"`
}

type renderCheckAPIResponse struct {
	Screenshot struct {
		Data   string `json:"data"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"screenshot"`
	ContentHeight   int `json:"contentHeight"`
	ConsoleMessages []struct {
		Level string `json:"level"`
		Text  string `json:"text"`
	} `json:"consoleMessages"`
	Network string `json:"network"`
}

// renderCheckOfflineNote tells the agent why remote resources did not load
// in a check that ran without network.
const renderCheckOfflineNote = "The check ran without network access, as your sandbox has none. Remote resources did not load."

func newRenderCommand(ctx *commandContext) *cobra.Command {
	var title string
	var height int
	var check bool
	var artifact bool
	var width int
	var out string
	var lint, guide bool
	cmd := &cobra.Command{
		Use:   "render <file.html>",
		Short: "Show a self-contained HTML page inline in this chat session's thread",
		Long: "Show a chart, table, diagram, or mockup inline in the current chat session's\n" +
			"thread, above your final reply. Call it before that reply, and do not announce\n" +
			"or restate the page in it.\n\n" +
			"The file must be one self-contained HTML document, at most 1 MiB; relative\n" +
			"URLs do not resolve. A local image given by absolute path (src=\"/abs/shot.png\"\n" +
			"or a JS string) is put into the page, up to 10 MiB each. AO stores its own\n" +
			"copy, so write the file outside the repository.\n\n" +
			"The page must follow the AO page design rules (`ao render --guide` prints\n" +
			"them): semantic HTML and AO's ao- classes, with no styles, colors, or fonts\n" +
			"of its own. AO adds its stylesheet and the reader's live theme, and refuses\n" +
			"a page that breaks a rule. `ao render --lint <file>` checks a page without\n" +
			"the daemon, in any session; run it on HTML artifacts too. Showing a page is\n" +
			"for chat sessions only: in a terminal session use `ao preview` instead.\n\n" +
			"Run `ao render --check <file>` first: the AO desktop app returns a\n" +
			"screenshot, the page's content height, its console messages, and the\n" +
			"design findings.",
		Example: `  ao render --guide
  ao render --lint "$TMPDIR/turns-by-day.html"
  ao render --check "$TMPDIR/turns-by-day.html" --width 390
  ao render "$TMPDIR/turns-by-day.html" --title "Turns by day" --height 420`,
		Args: func(cmd *cobra.Command, args []string) error {
			if guide {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if guide {
				_, err := fmt.Fprint(cmd.OutOrStdout(), renderpage.Guide)
				return err
			}
			if lint {
				return lintRender(cmd, args[0])
			}
			if check {
				return ctx.checkRender(cmd, args[0], width, out)
			}
			if strings.TrimSpace(title) == "" {
				return usageError{errors.New("--title is required unless --check is set")}
			}
			return ctx.publishRender(cmd, args[0], title, height, artifact)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "short name for the page (required unless --check)")
	cmd.Flags().IntVar(&height, "height", defaultRenderHeight, "first-paint frame height in CSS pixels, 80-2000; the frame then fits the page")
	cmd.Flags().BoolVar(&check, "check", false, "screenshot the page in the AO desktop app instead of publishing it")
	cmd.Flags().BoolVar(&artifact, "artifact", false, "also keep the page as a session artifact; only when the user asks to keep it")
	cmd.Flags().IntVar(&width, "width", defaultRenderWidth, "with --check: viewport width in CSS pixels, 240-1600; use 390 for phones")
	cmd.Flags().StringVar(&out, "out", "", "with --check: PNG path to write (default: a new file in the temp directory)")
	cmd.Flags().BoolVar(&lint, "lint", false, "check the page against the AO page design rules without publishing it; works in any session")
	cmd.Flags().BoolVar(&guide, "guide", false, "print the AO page design rules")
	cmd.MarkFlagsMutuallyExclusive("lint", "check", "guide")
	return cmd
}

// lintRender reports every place a page breaks the AO page design rules, and
// fails when there is one.
func lintRender(cmd *cobra.Command, file string) error {
	html, err := readRenderFile(file)
	if err != nil {
		return err
	}
	if findings := renderpage.Lint(html); len(findings) > 0 {
		return errors.New(renderpage.FindingsError(findings))
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "The page follows the AO page design rules.")
	return err
}

func renderSessionID() (string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", usageError{errors.New("ao render must run inside an AO chat session (AO_SESSION_ID is not set)")}
	}
	return sessionID, nil
}

func readRenderFile(file string) (string, error) {
	info, err := os.Stat(file)
	if err != nil {
		return "", usageError{fmt.Errorf("read %s: %w", file, err)}
	}
	if info.Size() > maxRenderFileBytes {
		return "", usageError{fmt.Errorf("%s is %d bytes; ao render accepts at most %d", file, info.Size(), maxRenderFileBytes)}
	}
	html, err := os.ReadFile(file)
	if err != nil {
		return "", usageError{fmt.Errorf("read %s: %w", file, err)}
	}
	return string(html), nil
}

func (c *commandContext) publishRender(cmd *cobra.Command, file, title string, height int, artifact bool) error {
	sessionID, err := renderSessionID()
	if err != nil {
		return err
	}
	html, err := readRenderFile(file)
	if err != nil {
		return err
	}
	resp, err := c.postRender(cmd.Context(), sessionID, html, title, height, artifact)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), renderShownText(resp.RenderID)); err != nil {
		return err
	}
	line := renderArtifactLine(resp)
	if line == "" {
		return nil
	}
	out := cmd.OutOrStdout()
	if resp.ArtifactError != "" {
		out = cmd.ErrOrStderr()
	}
	_, err = fmt.Fprintln(out, line)
	return err
}

// postRender inlines a page's local images and shows it in the session's
// thread. ao render and the html_render MCP tool both publish through it.
func (c *commandContext) postRender(ctx context.Context, sessionID, html, title string, height int, artifact bool) (renderAPIResponse, error) {
	var resp renderAPIResponse
	html, missing, err := inlineLocalImages(html)
	if err != nil {
		return resp, usageError{err}
	}
	if len(missing) > 0 {
		return resp, usageError{missingImagesError(missing)}
	}
	path := "sessions/" + url.PathEscape(sessionID) + "/renders"
	err = c.postJSON(ctx, path, renderAPIRequest{HTML: html, Title: title, Height: height, Artifact: artifact}, &resp)
	return resp, err
}

func renderShownText(renderID string) string {
	return fmt.Sprintf("Shown above your reply (render %s). Do not describe the page; add only what it does not say.", renderID)
}

// renderArtifactLine reports where a publish kept the page as an artifact, or
// why it did not; it is empty when no artifact was asked for.
func renderArtifactLine(resp renderAPIResponse) string {
	switch {
	case resp.ArtifactPath != "":
		return "saved as artifact: " + resp.ArtifactPath
	case resp.ArtifactError != "":
		return "warning: not saved as artifact: " + resp.ArtifactError
	}
	return ""
}

// postRenderCheck inlines a page's local images and has the desktop app
// screenshot it. A check shows what an unreadable image does to the page
// instead of refusing it, so missing lists those images.
func (c *commandContext) postRenderCheck(ctx context.Context, sessionID, html string, width int) (resp renderCheckAPIResponse, missing []string, err error) {
	html, missing, err = inlineLocalImages(html)
	if err != nil {
		return resp, nil, usageError{err}
	}
	path := "sessions/" + url.PathEscape(sessionID) + "/renders/check"
	err = c.postJSON(ctx, path, renderCheckAPIRequest{HTML: html, Width: width}, &resp)
	return resp, missing, err
}

func (c *commandContext) checkRender(cmd *cobra.Command, file string, width int, out string) error {
	sessionID, err := renderSessionID()
	if err != nil {
		return err
	}
	html, err := readRenderFile(file)
	if err != nil {
		return err
	}
	resp, missing, err := c.postRenderCheck(cmd.Context(), sessionID, html, width)
	if err != nil {
		return err
	}
	if out == "" {
		out = filepath.Join(os.TempDir(), fmt.Sprintf("ao-render-check-%d.png", time.Now().UnixNano()))
	}
	saved, err := writeRenderCheckScreenshot(resp.Screenshot.Data, out)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(w, "Saved %s (%dx%d)\n", saved, resp.Screenshot.Width, resp.Screenshot.Height); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content height: %d px at width %d.\n", resp.ContentHeight, width); err != nil {
		return err
	}
	if resp.Network == "none" {
		if _, err := fmt.Fprintln(w, renderCheckOfflineNote); err != nil {
			return err
		}
	}
	if findings := renderpage.Lint(html); len(findings) > 0 {
		if _, err := fmt.Fprintln(w, renderpage.FindingsError(findings)); err != nil {
			return err
		}
	}
	// Image paths and console text come from the page and from any script it
	// loads, so they are marked as untrusted the way `ao browser console` marks them.
	lines := make([]string, 0, len(resp.ConsoleMessages)+1)
	if len(missing) > 0 {
		lines = append(lines, "missing images: "+strings.Join(missing, ", "))
	}
	for _, m := range resp.ConsoleMessages {
		lines = append(lines, fmt.Sprintf("console.%s: %s", m.Level, m.Text))
	}
	if len(lines) == 0 {
		return nil
	}
	_, err = fmt.Fprintln(w, browserUntrustedText(strings.Join(lines, "\n")))
	return err
}

// writeRenderCheckScreenshot writes the check's PNG to target and returns its
// absolute path. Unlike `ao browser screenshot`, it replaces an earlier file at
// target, so the check, fix, check-again loop can reuse one --out path. Only a
// regular file is replaced, never a symlink or a directory, and the new file
// lands with one rename.
func writeRenderCheckScreenshot(encoded, target string) (string, error) {
	if encoded == "" {
		return "", errors.New("render check returned an empty screenshot")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode render check screenshot: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(abs); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to replace %s: it is not a regular file", abs)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".ao-render-check-*.png")
	if err != nil {
		return "", err
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return abs, nil
}
