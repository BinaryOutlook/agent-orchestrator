package controllers

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// agentBrowserRoute reports whether the desktop may reach this agent browser
// path: the stream viewer's page, its socket, and its file transfers. The
// agent's own command route and the browser's debugging routes stay on the
// host.
func agentBrowserRoute(tail string) bool {
	switch tail {
	case "v/cdp", "ws/cdp", "stats", "upload":
		return true
	}
	guid, ok := strings.CutPrefix(tail, "download/")
	return ok && guid != "" && !strings.Contains(guid, "/")
}

// agentBrowser streams the host's agent browser (AO_AGENT_BROWSER_URL) to the
// desktop as the session's agent tab, over the daemon's own authenticated
// connection. The browser's token is added here: clients never hold it, and
// the browser's port never leaves the host.
// ponytail: one agent browser per host, shown in each of its sessions; route per session when sessions get their own browser.
func (c *SessionsController) agentBrowser(w http.ResponseWriter, r *http.Request) {
	if c.AgentBrowser == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "AGENT_BROWSER_NOT_FOUND", "This host has no agent browser", nil)
		return
	}
	if _, err := c.Svc.GetRecord(r.Context(), sessionID(r)); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	tail, _, err := wildcardPath(r)
	if err != nil || !agentBrowserRoute(tail) {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "AGENT_BROWSER_NOT_FOUND", "Not an agent browser route", nil)
		return
	}
	target, token := c.AgentBrowser, c.AgentBrowserToken
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			req.Out.Host = target.Host
			req.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + "/" + tail
			req.Out.URL.RawPath = ""
			query := req.In.URL.Query()
			query.Set("token", token)
			req.Out.URL.RawQuery = query.Encode()
			// AO's credential and cookies are for the daemon. The desktop's
			// preview origin is not the browser's, whose socket check would
			// refuse the stream: the request arrives as the host's own.
			for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Origin", browserCapabilityHeader} {
				req.Out.Header.Del(name)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Agent browser unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}
