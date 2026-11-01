package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWiFiListenerMovesAndRevokesConnections(t *testing.T) {
	r := testRunner(t)
	r.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "viewer") })}
	defer r.setViewerNetwork("", "", "")
	// Separate loopback addresses model DHCP changes without needing a live LAN.
	if err := r.setViewerNetwork("127.0.0.2", "camfreenest.local", "ready"); err != "" {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	check := func(ip string, reachable bool) {
		t.Helper()
		res, err := client.Get("http://" + ip + ":8767/")
		if res != nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		if reachable && (err != nil || res.StatusCode != 200) {
			t.Fatalf("%s unavailable: %v", ip, err)
		}
		if !reachable && err == nil {
			t.Fatalf("viewer still reachable on %s", ip)
		}
	}
	check("127.0.0.2", true)
	check("127.0.0.1", false) // Must never use a wildcard listener.
	if err := r.setViewerNetwork("127.0.0.3", "camfreenest.local", "ready"); err != "" {
		t.Fatal(err)
	}
	check("127.0.0.2", false)
	check("127.0.0.3", true)
	r.setViewerNetwork("", "", "offline")
	check("127.0.0.3", false)
}

func TestDiscoveryLinksOnlyUseBoundWiFiAddress(t *testing.T) {
	r := testRunner(t)
	r.wifiIP, r.viewerHostname = "192.168.1.5", "camfreenest.local"
	request := httptest.NewRequest("GET", "http://127.0.0.1:8767/api/access", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	r.viewerAccess(w, request)
	var access struct {
		URLs []string `json:"urls"`
	}
	json.Unmarshal(w.Body.Bytes(), &access)
	if len(access.URLs) != 2 || !strings.HasPrefix(access.URLs[0], "http://camfreenest.local:8767/view?key=") || !strings.Contains(access.URLs[1], "192.168.1.5") {
		t.Fatal(w.Body.String())
	}
	r.wifiIP = ""
	w = httptest.NewRecorder()
	r.viewerAccess(w, request)
	json.Unmarshal(w.Body.Bytes(), &access)
	if len(access.URLs) != 0 {
		t.Fatal("offline link exposed")
	}
}

func TestHostnamePairingSurvivesRemoteIPChange(t *testing.T) {
	r := testRunner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.home)
	mux.HandleFunc("/api/config", r.config)
	handler := r.viewerGuard(mux)
	request := httptest.NewRequest("GET", "http://camfreenest.local:8767/view?key="+r.viewerToken, nil)
	request.RemoteAddr = "192.168.1.8:2000"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("hostname pairing failed")
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Domain != "" {
		t.Fatal("session should be host scoped")
	}
	request = httptest.NewRequest("GET", "http://camfreenest.local:8767/", nil)
	request.RemoteAddr = "192.168.1.21:3000"
	request.AddCookie(cookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal("session depended on IP")
	}
	request.URL.Path = "/api/config"
	// Even a .local request arriving from loopback requires pairing and cannot
	// use the privileged local Google settings routes.
	request.RemoteAddr = "127.0.0.1:3000"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("hostname got privileged local access")
	}
}

func TestViewerRejectsPublicWildcardAndInvalidAddresses(t *testing.T) {
	for _, ip := range []string{"0.0.0.0", "8.8.8.8", "100.64.0.1", "::", "::1", "127.0.0.1", "garbage"} {
		if SetViewerNetwork(ip, "", "") == "" {
			t.Fatalf("accepted %s", ip)
		}
	}
	for _, name := range []string{"camfreenest.local", "camfreenest-2.local"} {
		if !validViewerHostname(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"attacker.com", "a.b.local", "-x.local", ".local", "a/local"} {
		if validViewerHostname(name) {
			t.Fatal(name)
		}
	}
}
