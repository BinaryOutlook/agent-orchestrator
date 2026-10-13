package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Native UI that headless Chromium never paints, handed to the viewer: date
// and color pickers, the browser's context menu, permission prompts, HTTP
// basic auth, downloads and IME composition. The page half reports through
// the __rbUI binding; the viewer draws its own native control and answers.
const uiShim = `(() => {
  const report = (o) => window.__rbUI && window.__rbUI(JSON.stringify(o));
  // The page's icon, for a host app that shows this tab as its own: told on
  // every install, so a view attaching to a loaded page learns it too.
  if (window === top && location.origin !== 'null') {
    const icon = () => report({ kind: 'favicon', href: (document.querySelector('link[rel~="icon"]') || {}).href || location.origin + '/favicon.ico' });
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', icon, { once: true }); else icon();
  }
  if (window.__rbUIInstalled) return; window.__rbUIInstalled = true;
  let n = 0;
  const pickers = new Set(['color', 'date', 'time', 'datetime-local', 'month', 'week']);
  document.addEventListener('mousedown', (e) => {
    const el = e.target && e.target.closest && e.target.closest('input');
    if (!el || !pickers.has(el.type) || el.disabled || el.readOnly || e.button !== 0) return;
    const r = el.getBoundingClientRect();
    // Date fields open their calendar from the icon at the right edge, as in
    // Chrome; a click on the text keeps typing into its segments.
    if (el.type !== 'color' && e.clientX < r.right - 24) return;
    e.preventDefault(); el.focus();
    const id = el.dataset.rbPick || (el.dataset.rbPick = String(++n));
    report({ kind: 'picker', id, type: el.type, value: el.value, min: el.min, max: el.max, step: el.step, x: r.left, y: r.top, w: r.width, h: r.height });
  }, true);
  window.addEventListener('contextmenu', (e) => {
    setTimeout(() => {
      if (e.defaultPrevented) return; // the page drew its own menu
      const t = e.target, a = t.closest && t.closest('a[href]'), img = t.closest && t.closest('img');
      const editable = t.isContentEditable || ((t.tagName === 'INPUT' || t.tagName === 'TEXTAREA') && !t.readOnly && !t.disabled);
      report({ kind: 'contextmenu', x: e.clientX, y: e.clientY, link: a ? a.href : '', image: img ? img.src : '', selection: String(getSelection()), editable });
    }, 0);
  });
  let tipTimer = 0, tipFor = null;
  document.addEventListener('mouseover', (e) => {
    const el = e.target && e.target.closest && e.target.closest('[title]');
    if (el === tipFor) return;
    clearTimeout(tipTimer); tipFor = el;
    if (tipFor) report({ kind: 'tooltip', text: '' });
    if (el && el.title) tipTimer = setTimeout(() => report({ kind: 'tooltip', text: el.title, x: e.clientX, y: e.clientY + 18 }), 600);
  }, true);
  document.addEventListener('mousedown', () => { clearTimeout(tipTimer); report({ kind: 'tooltip', text: '' }); }, true);
  // The pointer shape under the mouse (hand on links, I-beam in fields).
  let cursor = '';
  document.addEventListener('mousemove', (e) => {
    const t = e.target;
    if (!(t instanceof Element)) return;
    const s = getComputedStyle(t);
    let c = s.cursor;
    if (c === 'auto') c = t.isContentEditable || t.matches('textarea, input:not([type=checkbox], [type=radio], [type=button], [type=submit], [type=range], [type=color], [type=file])') || overText(e, s) ? 'text' : 'default';
    if (c !== cursor) { cursor = c; report({ kind: 'cursor', cursor: c }); }
  }, true);
  // Chrome shows the I-beam over selectable text, not over the space around it.
  const overText = (e, s) => {
    if (s.userSelect === 'none') return false;
    const r = document.caretRangeFromPoint && document.caretRangeFromPoint(e.clientX, e.clientY), n = r && r.startContainer;
    if (!n || n.nodeType !== 3 || !n.textContent.trim()) return false;
    const all = document.createRange(); all.selectNodeContents(n);
    return [...all.getClientRects()].some((b) => e.clientX >= b.left && e.clientX <= b.right && e.clientY >= b.top && e.clientY <= b.bottom);
  };
  // What the page puts on the clipboard reaches the person's: Cmd+C and menu
  // Copy (after the page's own copy handler), and copy buttons that write
  // through the async clipboard API.
  const clip = (text, html) => text && report({ kind: 'clipboard', text, html: html || '' });
  for (const type of ['copy', 'cut']) window.addEventListener(type, (e) => {
    const d = e.clipboardData;
    if (e.defaultPrevented && d && d.getData('text/plain')) return clip(d.getData('text/plain'), d.getData('text/html'));
    const a = document.activeElement;
    if (a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA') && a.selectionStart != null) return clip(a.value.slice(a.selectionStart, a.selectionEnd));
    const sel = getSelection();
    let html = '';
    if (sel.rangeCount) { const div = document.createElement('div'); div.appendChild(sel.getRangeAt(0).cloneContents()); html = div.innerHTML; }
    clip(String(sel), html);
  });
  const cb = navigator.clipboard;
  if (cb) {
    const writeText = cb.writeText, write = cb.write;
    cb.writeText = function (t) { clip(String(t)); return writeText.call(this, t); };
    cb.write = function (items) {
      (async () => {
        for (const it of items) {
          if (!it.types.includes('text/plain')) continue;
          const html = it.types.includes('text/html') ? await (await it.getType('text/html')).text() : '';
          return clip(await (await it.getType('text/plain')).text(), html);
        }
      })().catch(() => {});
      return write.call(this, items);
    };
  }
  // Fullscreen changes how Chrome captures the page (rbhost crops the frames).
  if (window === top) document.addEventListener('fullscreenchange', () => report({ kind: 'fullscreen', on: !!document.fullscreenElement }));
  // Linux pastes the last selection on a middle click; a Mac does not.
  document.addEventListener('mouseup', (e) => { if (e.button === 1) e.preventDefault(); }, true);
  // <datalist> suggestions are a native popup too; the viewer draws them and
  // answers through pickerResp like the other pickers.
  const suggest = (el, open) => {
    const id = el.dataset.rbPick || (el.dataset.rbPick = String(++n));
    const q = el.value.toLowerCase(), r = el.getBoundingClientRect();
    const options = open ? [...el.list.options].map((o) => o.value).filter((v) => v && v !== el.value && v.toLowerCase().includes(q)).slice(0, 50) : [];
    report({ kind: 'datalist', id, options, x: r.left, y: r.bottom, w: r.width });
  };
  const listed = (t) => t instanceof HTMLInputElement && t.list;
  for (const [type, open] of [['focusin', true], ['input', true], ['focusout', false]]) {
    document.addEventListener(type, (e) => { if (listed(e.target)) suggest(e.target, open); }, true);
  }
  // A submit blocked by validation: Chrome's bubble is native UI as well.
  let invalidShown = false;
  document.addEventListener('invalid', (e) => {
    if (invalidShown) return;
    invalidShown = true; queueMicrotask(() => { invalidShown = false; });
    const r = e.target.getBoundingClientRect();
    report({ kind: 'tooltip', text: e.target.validationMessage, x: r.left, y: r.bottom + 4, hold: 4000 });
  }, true);
  const ask = (name) => new Promise((resolve) => { const id = String(++n); (window.__rbPerm ||= {})[id] = resolve; report({ kind: 'permission', id, name, origin: location.origin }); });
  window.__rbPermAnswer = (id, granted) => { const r = window.__rbPerm && window.__rbPerm[id]; if (r) { delete window.__rbPerm[id]; r(granted); } };
  if (window.Notification) {
    const request = Notification.requestPermission.bind(Notification);
    Notification.requestPermission = async (cb) => {
      let p = Notification.permission;
      if (p === 'default') p = (await ask('notifications')) ? await request() : 'denied';
      if (cb) cb(p);
      return p;
    };
    // Headless Chromium shows no notification: the viewer does.
    const N = window.Notification, shown = (title, o) => N.permission === 'granted' && report({ kind: 'notification', title: String(title), body: (o && o.body) || '', origin: location.origin });
    const Wrapped = function Notification(title, o) { const n = new N(title, o); shown(title, o); return n; };
    Wrapped.prototype = N.prototype;
    Object.defineProperty(Wrapped, 'permission', { get: () => N.permission });
    Wrapped.requestPermission = N.requestPermission;
    window.Notification = Wrapped;
    const reg = window.ServiceWorkerRegistration && ServiceWorkerRegistration.prototype, show = reg && reg.showNotification;
    if (show) reg.showNotification = function (title, o) { shown(title, o); return show.call(this, title, o); };
  }
  // Passkeys and security keys: headless Chromium has no authenticator and no
  // dialog, so the request hung for good. Refuse it at once, as cancelling
  // Chrome's dialog would, and tell the person why.
  // ponytail: also refuses an agent's CDP virtual authenticator; skip when one is set up if agents test passkeys.
  const creds = navigator.credentials;
  if (creds && creds.get) for (const op of ['create', 'get']) {
    const orig = creds[op].bind(creds);
    creds[op] = (o) => {
      if (!o || !o.publicKey || o.mediation === 'conditional') return orig(o);
      report({ kind: 'webauthn', origin: location.origin });
      return Promise.reject(new DOMException('The operation either timed out or was not allowed.', 'NotAllowedError'));
    };
  }
  if (navigator.geolocation) {
    const g = navigator.geolocation, get = g.getCurrentPosition.bind(g), watch = g.watchPosition.bind(g);
    let decided = null;
    const gate = () => (decided ??= navigator.permissions.query({ name: 'geolocation' }).then((s) => s.state === 'granted' || ask('geolocation')));
    const denied = (err) => err && err({ code: 1, message: 'User denied Geolocation', PERMISSION_DENIED: 1 });
    g.getCurrentPosition = (ok, err, opts) => { gate().then((y) => (y ? get(ok, err, opts) : denied(err))); };
    g.watchPosition = (ok, err, opts) => { let id = 0; gate().then((y) => (y ? (id = watch(ok, err, opts)) : denied(err))); return id; };
  }
})();`

var basicRealm = regexp.MustCompile(`(?i)^basic(?:\s+realm="([^"]*)")?`)

// onBridgeEvent handles the CDP events the bridges need. It runs on the CDP
// read goroutine, so it only Notifies (never Calls).
func (v *viewer) onBridgeEvent(sid string, m cdpMsg) {
	switch m.Method {
	case "Network.responseReceived":
		var p struct {
			Type     string `json:"type"`
			Response struct {
				URL     string            `json:"url"`
				Status  int               `json:"status"`
				Headers map[string]string `json:"headers"`
			} `json:"response"`
		}
		if json.Unmarshal(m.Params, &p) != nil || p.Type != "Document" || p.Response.Status != http.StatusUnauthorized {
			return
		}
		origin := originOf(p.Response.URL)
		v.mu.Lock()
		a, failed := v.auth[origin], v.authFailed[origin]
		delete(v.authFailed, origin)
		if a != nil && a.cancel { // after Cancel the server's 401 page stays, as in Chrome
			delete(v.auth, origin)
		}
		v.mu.Unlock()
		if a != nil && a.cancel {
			return
		}
		for k, val := range p.Response.Headers {
			if strings.EqualFold(k, "www-authenticate") {
				if mm := basicRealm.FindStringSubmatch(val); mm != nil {
					v.sendJSON(map[string]any{"t": "auth", "origin": origin, "url": p.Response.URL, "realm": mm[1], "failed": failed})
				}
			}
		}
	case "Fetch.requestPaused":
		var p struct {
			RequestID          string     `json:"requestId"`
			ResponseStatusCode int        `json:"responseStatusCode"`
			ResponseHeaders    []fetchHdr `json:"responseHeaders"`
			Request            struct {
				URL string `json:"url"`
			} `json:"request"`
		}
		_ = json.Unmarshal(m.Params, &p)
		v.mu.Lock()
		a := v.auth[originOf(p.Request.URL)]
		v.mu.Unlock()
		if p.ResponseStatusCode == http.StatusUnauthorized && a != nil && a.cancel {
			go v.show401(sid, p.RequestID, p.ResponseHeaders)
			return
		}
		v.h.cdp.Notify(sid, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID})
	case "Fetch.authRequired":
		var p struct {
			RequestID     string `json:"requestId"`
			AuthChallenge struct {
				Origin string `json:"origin"`
				Scheme string `json:"scheme"`
			} `json:"authChallenge"`
		}
		_ = json.Unmarshal(m.Params, &p)
		resp := map[string]any{"response": "CancelAuth"}
		v.mu.Lock()
		a := v.auth[p.AuthChallenge.Origin]
		switch {
		case a == nil || a.cancel || !strings.EqualFold(p.AuthChallenge.Scheme, "basic"):
		case v.authTried[p.RequestID]:
			// The same request challenged again: the server refused these
			// credentials. Ask the person again, rather than resend them until
			// Chrome gives up (62 tries in 4 s, enough to lock an account).
			delete(v.auth, p.AuthChallenge.Origin)
			delete(v.authTried, p.RequestID)
			v.authFailed[p.AuthChallenge.Origin] = true
		default:
			v.authTried[p.RequestID] = true
			resp = map[string]any{"response": "ProvideCredentials", "username": a.user, "password": a.pass}
		}
		v.mu.Unlock()
		v.h.cdp.Notify(sid, "Fetch.continueWithAuth", map[string]any{"requestId": p.RequestID, "authChallengeResponse": resp})
	}
}

// authEntry is the person's answer to an origin's basic-auth prompt.
type authEntry struct {
	user, pass string
	cancel     bool // Cancel: load the server's 401 page instead of an error page
}

type fetchHdr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// show401 serves the server's own 401 page, which Chrome shows once its login
// prompt is cancelled. Headless Chromium shows an empty error page for any
// unanswered challenge (CancelAuth included), so the page goes through
// without the challenge header.
func (v *viewer) show401(sid, id string, headers []fetchHdr) {
	ctx, cancel := context.WithTimeout(v.ctx, 10*time.Second)
	defer cancel()
	res, err := v.h.cdp.Call(ctx, sid, "Fetch.getResponseBody", map[string]any{"requestId": id})
	var b struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err != nil || json.Unmarshal(res, &b) != nil {
		v.h.cdp.Notify(sid, "Fetch.continueRequest", map[string]any{"requestId": id})
		return
	}
	if !b.Base64Encoded {
		b.Body = base64.StdEncoding.EncodeToString([]byte(b.Body))
	}
	keep := []fetchHdr{}
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "www-authenticate") {
			keep = append(keep, h)
		}
	}
	_, _ = v.h.cdp.Call(ctx, sid, "Fetch.fulfillRequest", map[string]any{"requestId": id, "responseCode": http.StatusUnauthorized, "responseHeaders": keep, "body": b.Body})
}

// onBridgeMessage handles the viewer's answers; true if it was one of them.
func (v *viewer) onBridgeMessage(raw []byte, kind string) bool {
	var m struct {
		ID       string  `json:"id"`
		Value    string  `json:"value"`
		Action   string  `json:"action"`
		URL      string  `json:"url"`
		Name     string  `json:"name"`
		Origin   string  `json:"origin"`
		Granted  bool    `json:"granted"`
		Lat      float64 `json:"lat"`
		Lon      float64 `json:"lon"`
		Username string  `json:"username"`
		Password string  `json:"password"`
		Text     string  `json:"text"`
		Start    int     `json:"start"`
		End      int     `json:"end"`
		Back     bool    `json:"back"`
		Fresh    bool    `json:"fresh"`
		Ctx      string  `json:"ctx"`
		Cancel   bool    `json:"cancel"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	switch kind {
	case "pickerResp":
		v.human()
		id, _ := json.Marshal(m.ID)
		val, _ := json.Marshal(m.Value)
		// The native setter, so frameworks that wrap .value (React) see the change.
		// In the frame that asked: ids restart at 1 in every frame.
		go v.evalIn(m.Ctx, `(() => { const el = document.querySelector('[data-rb-pick="' + `+string(id)+` + '"]'); if (!el) return false;
			Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, `+string(val)+`);
			el.dispatchEvent(new Event('input', { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`, false)
	case "find":
		// Headless Chromium has no find bar: window.find selects and scrolls to
		// the next match (wrapping), so the stream shows it like Chrome's highlight.
		text, _ := json.Marshal(m.Text)
		go func() {
			res, err := v.call("Runtime.evaluate", map[string]any{"returnByValue": true, "expression": fmt.Sprintf(
				`(() => { const t = %s; if (%t || !t) getSelection().removeAllRanges(); return !!t && window.find(t, false, %t, true, false, false, false); })()`,
				text, m.Fresh, m.Back)})
			var r struct {
				Result struct {
					Value bool `json:"value"`
				} `json:"result"`
			}
			if err == nil {
				_ = json.Unmarshal(res, &r)
			}
			v.sendJSON(map[string]any{"t": "found", "text": m.Text, "found": r.Result.Value})
		}()
	case "menu":
		v.human()
		go v.menuAction(m.Ctx, m.Action, m.URL)
	case "permResp":
		go v.answerPermission(m.Ctx, m.ID, m.Name, m.Origin, m.Granted, m.Lat, m.Lon)
	case "authResp":
		v.human()
		go v.answerAuth(m.Origin, &authEntry{m.Username, m.Password, m.Cancel})
	case "ime":
		v.human()
		v.mu.Lock()
		sid := v.session
		v.mu.Unlock()
		v.input(func() {
			v.h.cdp.Notify(sid, "Input.imeSetComposition", map[string]any{"text": m.Text, "selectionStart": m.Start, "selectionEnd": m.End})
		})
	default:
		return false
	}
	return true
}

func (v *viewer) menuAction(ctxKey, action, url string) {
	switch action {
	case "copy", "cut", "selectAll": // a copy's text reaches the desktop through the shim's copy listener
		v.evalIn(ctxKey, "document.execCommand('"+action+"')", true)
	case "openLink":
		ctx, cancel := context.WithTimeout(v.ctx, 10*time.Second)
		defer cancel()
		_, _ = v.h.cdp.Call(ctx, "", "Target.createTarget", map[string]any{"url": url})
	}
}

func (v *viewer) answerPermission(ctxKey, id, name, origin string, granted bool, lat, lon float64) {
	if granted {
		ctx, cancel := context.WithTimeout(v.ctx, 10*time.Second)
		defer cancel()
		// setPermission leaves the origin's other permissions as they were (grantPermissions denies them).
		if _, err := v.h.cdp.Call(ctx, "", "Browser.setPermission", map[string]any{"origin": origin, "permission": map[string]any{"name": name}, "setting": "granted"}); err != nil {
			log.Printf("grant %s: %v", name, err)
			granted = false
		}
		if name == "geolocation" && granted {
			// Headless Chromium has no location source: the viewer supplies one.
			_, _ = v.call("Emulation.setGeolocationOverride", map[string]any{"latitude": lat, "longitude": lon, "accuracy": 100})
		}
	}
	idJSON, _ := json.Marshal(id)
	v.evalIn(ctxKey, "window.__rbPermAnswer && window.__rbPermAnswer("+string(idJSON)+", "+map[bool]string{true: "true", false: "false"}[granted]+")", false)
}

// answerAuth intercepts only the challenging origin's requests, answers its
// challenge with the person's credentials (or cancels it), and reloads.
func (v *viewer) answerAuth(origin string, a *authEntry) {
	v.mu.Lock()
	if v.auth == nil {
		v.auth, v.authTried, v.authFailed = map[string]*authEntry{}, map[string]bool{}, map[string]bool{}
	}
	v.auth[origin] = a
	origins := make([]map[string]any, 0, len(v.auth))
	for o, e := range v.auth {
		p := map[string]any{"urlPattern": o + "/*"}
		if e.cancel { // its 401 page is caught as a response (show401)
			p["requestStage"], p["resourceType"] = "Response", "Document"
		}
		origins = append(origins, p)
	}
	v.mu.Unlock()
	if _, err := v.call("Fetch.enable", map[string]any{"handleAuthRequests": true, "patterns": origins}); err != nil {
		log.Printf("basic auth: %v", err)
		return
	}
	_, _ = v.call("Page.reload", nil)
}

func originOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		if j := strings.IndexByte(u[i+3:], '/'); j >= 0 {
			return u[:i+3+j]
		}
	}
	return u
}

// downloads saves the remote browser's downloads and serves them to viewers.
type downloads struct {
	dir   string
	mu    sync.Mutex
	names map[string]string // guid -> suggested name
}

const downloadDir = "/tmp/rb-downloads"

func (h *host) startDownloads(ctx context.Context) {
	h.dl = &downloads{dir: downloadDir, names: map[string]string{}}
	_ = os.MkdirAll(downloadDir, 0o700)
	if _, err := h.cdp.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "allowAndName", "downloadPath": downloadDir, "eventsEnabled": true}); err != nil {
		log.Printf("downloads: %v", err)
		return
	}
	h.cdp.On(func(m cdpMsg) {
		switch m.Method {
		case "Browser.downloadWillBegin":
			var p struct {
				GUID              string `json:"guid"`
				SuggestedFilename string `json:"suggestedFilename"`
			}
			_ = json.Unmarshal(m.Params, &p)
			h.dl.mu.Lock()
			h.dl.names[p.GUID] = p.SuggestedFilename
			h.dl.mu.Unlock()
		case "Browser.downloadProgress":
			var p struct {
				GUID       string  `json:"guid"`
				State      string  `json:"state"`
				TotalBytes float64 `json:"totalBytes"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.State != "completed" {
				return
			}
			h.dl.mu.Lock()
			name := h.dl.names[p.GUID]
			h.dl.mu.Unlock()
			msg, _ := json.Marshal(map[string]any{"t": "download", "guid": p.GUID, "name": name, "size": p.TotalBytes})
			h.eachViewer(func(v *viewer) { v.sendRaw(msg) })
		}
	})
}

func (h *host) handleDownload(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	h.dl.mu.Lock()
	name, ok := h.dl.names[guid]
	h.dl.mu.Unlock()
	if !ok || strings.ContainsAny(guid, `/\.`) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+strings.ReplaceAll(strings.ReplaceAll(name, `"`, ""), " ", "%20"))
	http.ServeFile(w, r, filepath.Join(h.dl.dir, guid))
}
