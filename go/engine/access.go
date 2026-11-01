package engine

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type accessContextKey struct{}

const adminRole = "admin"
const viewerRole = "viewer"

func createAccessToken() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func readAccessToken(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e == nil && len(b) == 32 {
		if _, decodeErr := hex.DecodeString(string(b)); decodeErr == nil {
			return string(b), nil
		}
	}
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	value, e := createAccessToken()
	if e != nil {
		return "", e
	}
	return value, os.WriteFile(path, []byte(value), 0600)
}
func (r *Runner) initViewerToken() error {
	// Migrate older shared links and cookies to viewing-only access. Native
	// localhost administration remains available; remote admins get a new key.
	var e error
	r.viewerToken, e = readAccessToken(filepath.Join(r.dir, "viewer-token"))
	if e != nil {
		return e
	}
	r.adminToken, e = readAccessToken(filepath.Join(r.dir, "admin-token"))
	if e != nil {
		return e
	}
	if r.adminToken == r.viewerToken {
		r.adminToken, e = createAccessToken()
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(r.dir, "admin-token"), []byte(r.adminToken), 0600)
	}
	return nil
}
func localRequest(req *http.Request) bool {
	remote, _, e := net.SplitHostPort(req.RemoteAddr)
	if e != nil || !net.ParseIP(remote).IsLoopback() {
		return false
	}
	host := req.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}
func tokenMatches(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func requestRole(req *http.Request) string {
	role, _ := req.Context().Value(accessContextKey{}).(string)
	return role
}
func viewerResource(path string) bool {
	return path == "/" || path == "/view" || path == "/api/clips" || path == "/api/access" || strings.HasPrefix(path, "/clip/") || strings.HasPrefix(path, "/preview/")
}
func (r *Runner) viewerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		// Static branding assets contain no user data and are needed by the
		// pairing error page as well as both authenticated pages.
		if strings.HasPrefix(req.URL.Path, "/assets/") && (req.Method == "GET" || req.Method == "HEAD") {
			next.ServeHTTP(w, req)
			return
		}
		if req.Method != "GET" && req.Method != "HEAD" {
			if origin := req.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != req.Host || u.Scheme != "http" {
					http.Error(w, "Use this page's own controls", 403)
					return
				}
			}
		}
		role := ""
		if localRequest(req) {
			role = adminRole
		} else {
			r.tokenMu.RLock()
			admin, viewer := r.adminToken, r.viewerToken
			r.tokenMu.RUnlock()
			page := req.URL.Path == "/" || req.URL.Path == "/admin" || req.URL.Path == "/view"
			if key := req.URL.Query().Get("key"); page && req.Method == "GET" && key != "" {
				cookie := ""
				switch {
				case req.URL.Path != "/view" && tokenMatches(key, admin):
					role = adminRole
					cookie = "camfreenest_admin"
				case req.URL.Path != "/admin" && tokenMatches(key, viewer):
					role = viewerRole
					cookie = "camfreenest_viewer"
				}
				if role == "" {
					http.Error(w, "Invalid link for this page. Copy a current viewer or admin link from the recording phone.", 401)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: cookie, Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
			} else {
				if c, e := req.Cookie("camfreenest_admin"); e == nil && tokenMatches(c.Value, admin) {
					role = adminRole
				}
				if role == "" {
					if c, e := req.Cookie("camfreenest_viewer"); e == nil && tokenMatches(c.Value, viewer) {
						role = viewerRole
					}
				}
			}
			if role == "" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(401)
				fmt.Fprint(w, `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>CamSaver · Private recordings</title><link rel="stylesheet" href="/assets/app.css"><main><div class="empty"><img src="/assets/icon-192.png" alt=""><strong>This browser needs a private link</strong><p>You reached the recording phone. Open the complete viewer link shared by its owner.</p><p>Admin access uses a separate private link.</p></div></main></html>`)
				return
			}
			if role == viewerRole && ((req.Method != "GET" && req.Method != "HEAD") || !viewerResource(req.URL.Path)) {
				http.Error(w, "View-only access. Use a private admin link to control the recorder.", 403)
				return
			}
			switch req.URL.Path {
			case "/api/config", "/api/test", "/api/oauth/link", "/api/oauth/complete", "/api/subscription/test", "/api/events/repair", "/api/access/reset-viewer":
				http.Error(w, "Manage Google settings and sharing on the recording phone", 403)
				return
			}
		}
		req = req.WithContext(context.WithValue(req.Context(), accessContextKey{}, role))
		next.ServeHTTP(w, req)
	})
}
func (r *Runner) viewerAccess(w http.ResponseWriter, req *http.Request) {
	local := localRequest(req)
	viewURLs, adminURLs := []string{}, []string{}
	role := requestRole(req)
	if local {
		role = adminRole
	}
	if role == "" {
		role = viewerRole
	}
	status := ""
	if local {
		r.tokenMu.RLock()
		admin, viewer := r.adminToken, r.viewerToken
		r.tokenMu.RUnlock()
		r.networkMu.Lock()
		ip, host := r.wifiIP, r.viewerHostname
		status = r.discoveryStatus
		r.networkMu.Unlock()
		hosts := []string{}
		if ip != "" {
			if host != "" {
				hosts = append(hosts, host)
			}
			hosts = append(hosts, ip)
		}
		for _, h := range hosts {
			base := "http://" + net.JoinHostPort(h, "8767")
			viewURLs = append(viewURLs, base+"/view?key="+viewer)
			adminURLs = append(adminURLs, base+"/admin?key="+admin)
		}
	}
	r.writeJSON(w, 200, map[string]any{"local": local, "role": role, "urls": viewURLs, "viewerUrls": viewURLs, "adminUrls": adminURLs, "discovery": status})
}
func (r *Runner) resetViewerAccess(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	if !localRequest(req) {
		http.Error(w, "Reset sharing on the recording phone", 403)
		return
	}
	token, e := createAccessToken()
	if e != nil {
		r.writeJSON(w, 500, map[string]string{"error": "Could not create a new viewer link"})
		return
	}
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	path := filepath.Join(r.dir, "viewer-token")
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, []byte(token), 0600); e == nil {
		e = os.Rename(tmp, path)
	}
	if e != nil {
		r.writeJSON(w, 500, map[string]string{"error": "Could not save the viewer link"})
		return
	}
	r.viewerToken = token
	r.writeJSON(w, 200, map[string]string{"status": "Viewer link reset"})
}
