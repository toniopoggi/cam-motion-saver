package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shareRequest(method, path string, cookie *http.Cookie) *http.Request {
	req := remoteRequest(method, path)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}
func pairedCookie(t *testing.T, r *Runner, path, key string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.home)).ServeHTTP(w, remoteRequest("GET", path+"?key="+key))
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("pairing failed: %d %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}

func TestLegacySharedTokenBecomesViewOnly(t *testing.T) {
	r := testRunner(t)
	old := r.viewerToken
	admin := r.adminToken
	if old == admin || len(admin) != 32 {
		t.Fatal("tokens not independent")
	}
	r2 := &Runner{dir: r.dir}
	if e := r2.initViewerToken(); e != nil {
		t.Fatal(e)
	}
	if r2.viewerToken != old || r2.adminToken != admin {
		t.Fatal("token persistence failed")
	}
	cookie := pairedCookie(t, r, "/", old)
	if cookie.Name != "camfreenest_viewer" {
		t.Fatal("legacy token upgraded privilege")
	}
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.stopRecorder)).ServeHTTP(w, shareRequest("POST", "/api/recorder/stop", cookie))
	if w.Code != 403 || !r.recorderEnabled.Load() {
		t.Fatal("old shared link still controls recorder")
	}
	for _, name := range []string{"viewer-token", "admin-token"} {
		info, e := os.Stat(filepath.Join(r.dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("token not stored privately")
		}
	}
}
func TestReadOnlyPageAndEveryMutationAreProtected(t *testing.T) {
	r := testRunner(t)
	cookie := pairedCookie(t, r, "/view", r.viewerToken)
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.home)).ServeHTTP(w, shareRequest("GET", "/view", cookie))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "View only") || !strings.Contains(body, "<video") {
		t.Fatal("viewer page missing")
	}
	for _, control := range []string{"manualStart", "recorderStop", "saveDuration", "admin.js", "OAuthResult", "repairEvents"} {
		if strings.Contains(body, control) {
			t.Fatalf("viewer includes %s", control)
		}
	}
	endpoints := []string{"/api/recording/start", "/api/recording/stop", "/api/recorder/start", "/api/recorder/stop", "/api/capture", "/api/recording/settings", "/api/config", "/api/test", "/api/oauth/link", "/api/oauth/complete", "/api/subscription/test", "/api/events/repair", "/api/preview/retry", "/api/access/reset-viewer"}
	for _, path := range endpoints {
		for _, method := range []string{"GET", "HEAD", "POST"} {
			w = httptest.NewRecorder()
			r.viewerGuard(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { t.Fatalf("viewer reached %s %s", method, path) })).ServeHTTP(w, shareRequest(method, path, cookie))
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/status", "/api/events", "/admin"} {
		w = httptest.NewRecorder()
		r.viewerGuard(http.HandlerFunc(r.home)).ServeHTTP(w, shareRequest("GET", path, cookie))
		if w.Code != 403 {
			t.Fatalf("private resource accessible: %s", path)
		}
	}
}
func TestReadOnlyClipRangeAndAccessMetadata(t *testing.T) {
	r := testRunner(t)
	cookie := pairedCookie(t, r, "/view", r.viewerToken)
	os.WriteFile(filepath.Join(r.dir, "clips", "bird.mp4"), []byte("0123456789abcdef"), 0600)
	req := shareRequest("GET", "/clip/bird.mp4", cookie)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.clip)).ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "2345" {
		t.Fatal("read-only range playback failed")
	}
	w = httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.viewerAccess)).ServeHTTP(w, shareRequest("GET", "/api/access", cookie))
	if w.Code != 200 || strings.Contains(w.Body.String(), r.adminToken) || strings.Contains(w.Body.String(), r.viewerToken) {
		t.Fatal("sharing metadata leaked credentials")
	}
	var info struct {
		Role  string
		Local bool
		URLs  []string
	}
	json.Unmarshal(w.Body.Bytes(), &info)
	if info.Role != viewerRole || info.Local || len(info.URLs) != 0 {
		t.Fatal(w.Body.String())
	}
}
func TestAdminAndViewerKeysAreBoundToTheirPages(t *testing.T) {
	r := testRunner(t)
	for _, entry := range []struct{ path, key string }{{"/admin", r.viewerToken}, {"/view", r.adminToken}} {
		w := httptest.NewRecorder()
		r.viewerGuard(http.HandlerFunc(r.home)).ServeHTTP(w, remoteRequest("GET", entry.path+"?key="+entry.key))
		if w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatal("key accepted for wrong role page")
		}
	}
	cookie := pairedCookie(t, r, "/admin", r.adminToken)
	if cookie.Name != "camfreenest_admin" {
		t.Fatal("wrong admin cookie")
	}
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.stopRecorder)).ServeHTTP(w, shareRequest("POST", "/api/recorder/stop", cookie))
	if w.Code != 200 || r.recorderEnabled.Load() {
		t.Fatal("admin controls failed")
	}
	w = httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.config)).ServeHTTP(w, shareRequest("GET", "/api/config", cookie))
	if w.Code != 403 {
		t.Fatal("remote admin can read Google settings")
	}
}
func TestResetViewerLinkRevokesOldSessionOnly(t *testing.T) {
	r := testRunner(t)
	old := r.viewerToken
	admin := r.adminToken
	cookie := pairedCookie(t, r, "/view", old)
	req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/access/reset-viewer", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.resetViewerAccess)).ServeHTTP(w, req)
	if w.Code != 200 || r.viewerToken == old || r.adminToken != admin {
		t.Fatal("viewer reset changed wrong credential")
	}
	w = httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.home)).ServeHTTP(w, shareRequest("GET", "/view", cookie))
	if w.Code != 401 {
		t.Fatal("old viewer session remains valid")
	}
	_ = pairedCookie(t, r, "/view", r.viewerToken)
	r2 := &Runner{dir: r.dir}
	r2.initViewerToken()
	if r2.viewerToken != r.viewerToken || r2.adminToken != admin {
		t.Fatal("rotation not persisted")
	}
}
func TestGalleryPrecedesControlsOnAdminPage(t *testing.T) {
	r := testRunner(t)
	w := httptest.NewRecorder()
	r.home(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/admin", nil))
	body := w.Body.String()
	if strings.Index(body, `id="recordings"`) < 0 || strings.Index(body, `id="recordings"`) > strings.Index(body, `id="recorder"`) {
		t.Fatal("controls appear before recordings")
	}
	if !strings.Contains(body, `id="settings"`) || strings.Contains(body, `id="settings" open`) {
		t.Fatal("settings are not collapsed")
	}
}
