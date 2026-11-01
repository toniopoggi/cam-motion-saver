package engine

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"camfreenest/capture"
	"camfreenest/localmotion"
)

//go:embed web
var assets embed.FS

type Configuration struct {
	ClientID             string
	ClientSecret         string
	RefreshToken         string
	DeviceName           string
	Subscription         string
	ServiceAccountJSON   string
	DeviceAccessProject  string
	ClipSeconds          int
	LocalMotionDisabled  bool
	GoogleEventsDisabled bool
	MotionSensitivity    int
	MotionArea           *localmotion.Region
	RetentionDays        int64
}

type Event struct {
	Time    string
	Kind    string
	Message string
}

type Runner struct {
	motion            motionState
	delivery          eventDelivery
	viewerToken       string
	adminToken        string
	tokenMu           sync.RWMutex
	networkMu         sync.Mutex
	wifiServer        *http.Server
	wifiIP            string
	viewerHostname    string
	discoveryStatus   string
	recordingMu       sync.Mutex
	recorderEnabled   atomic.Bool
	manualWanted      atomic.Bool
	sessionMu         sync.Mutex
	sessionCancel     context.CancelFunc
	captureMu         sync.RWMutex
	configChanged     chan struct{}
	configMu          sync.Mutex
	previewMu         sync.Mutex
	captureController *capture.Controller
	packetLoss        atomic.Uint64
	eventCount        atomic.Uint64
	lastPubSubCheck   atomic.Int64
	lastIgnored       atomic.Value
	dir               string
	server            *http.Server
	cancel            context.CancelFunc
	busy              atomic.Bool
	mu                sync.Mutex
	logs              []Event
}

var stateMu sync.Mutex
var state *Runner

// Start sets up the embedded recorder once, on an app-private storage directory.
func Start(path string) string {
	stateMu.Lock()
	defer stateMu.Unlock()
	if state != nil {
		return ""
	}
	if err := os.MkdirAll(filepath.Join(path, "clips"), 0700); err != nil {
		return err.Error()
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{dir: path, cancel: cancel, configChanged: make(chan struct{}, 1)}
	runner.lastIgnored.Store("")
	runner.initMotion()
	if err := runner.initViewerToken(); err != nil {
		cancel()
		return err.Error()
	}
	paused, _ := os.ReadFile(filepath.Join(path, "recorder-paused"))
	runner.recorderEnabled.Store(string(paused) != "true")
	mux := http.NewServeMux()
	mux.HandleFunc("/", runner.home)
	mux.HandleFunc("/api/status", runner.status)
	mux.HandleFunc("/api/access", runner.viewerAccess)
	mux.HandleFunc("/api/access/reset-viewer", runner.resetViewerAccess)
	mux.HandleFunc("/assets/", runner.staticAsset)
	mux.HandleFunc("/api/recording/settings", runner.recordingSettings)
	mux.HandleFunc("/api/live/frame", runner.liveFrame)
	mux.HandleFunc("/api/preview/retry", runner.retryPreview)
	mux.HandleFunc("/api/recording/start", runner.startRecording)
	mux.HandleFunc("/api/recording/stop", runner.stopRecording)
	mux.HandleFunc("/api/recorder/start", runner.startRecorder)
	mux.HandleFunc("/api/recorder/stop", runner.stopRecorder)
	mux.HandleFunc("/preview/", runner.preview)
	mux.HandleFunc("/api/config", runner.config)
	mux.HandleFunc("/api/test", runner.testSDM)
	mux.HandleFunc("/api/oauth/link", runner.oauthLink)
	mux.HandleFunc("/api/oauth/complete", runner.oauthComplete)
	mux.HandleFunc("/api/subscription/test", runner.testSubscription)
	mux.HandleFunc("/api/events/repair", runner.repairEvents)
	mux.HandleFunc("/api/capture", runner.captureRequest)
	mux.HandleFunc("/api/clips", runner.clips)
	mux.HandleFunc("/api/events", runner.events)
	mux.HandleFunc("/clip/", runner.clip)
	runner.server = &http.Server{Addr: "127.0.0.1:8767", Handler: runner.viewerGuard(mux), ReadHeaderTimeout: 5 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:8767")
	if err != nil {
		cancel()
		return err.Error()
	}
	state = runner
	_ = runner.setViewerNetwork(viewerNetworkIP, viewerNetworkHost, viewerNetworkStatus)
	runner.log("info", "Local CamSaver recorder started")
	go func() { _ = runner.server.Serve(listener) }()
	go runner.liveLoop(ctx)
	go runner.pollLoop(ctx)
	go runner.cleanupLoop(ctx)
	return ""
}

// Stop gracefully ends the local server when Android destroys the service.
func Stop() {
	stateMu.Lock()
	runner := state
	state = nil
	stateMu.Unlock()
	if runner == nil {
		return
	}
	runner.networkMu.Lock()
	if runner.wifiServer != nil {
		_ = runner.wifiServer.Close()
	}
	runner.networkMu.Unlock()
	runner.cancel()
	runner.captureMu.RLock()
	ctl := runner.captureController
	runner.captureMu.RUnlock()
	if ctl != nil {
		ctl.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = runner.server.Shutdown(ctx)
}

func (r *Runner) log(kind, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, Event{Time: time.Now().Format(time.RFC3339), Kind: kind, Message: message})
	if len(r.logs) > 100 {
		r.logs = r.logs[len(r.logs)-100:]
	}
}
func (r *Runner) cfgPath() string { return filepath.Join(r.dir, "config.json") }
func (r *Runner) loadConfig() Configuration {
	var c Configuration
	b, err := os.ReadFile(r.cfgPath())
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.MotionSensitivity < 1 || c.MotionSensitivity > 5 {
		c.MotionSensitivity = 3
	}
	if c.ClipSeconds < 5 || c.ClipSeconds > 600 {
		c.ClipSeconds = 60
	}
	if c.RetentionDays < 1 {
		c.RetentionDays = 1
	}
	return c
}
func (r *Runner) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (r *Runner) home(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" && req.URL.Path != "/admin" && req.URL.Path != "/view" {
		http.NotFound(w, req)
		return
	}
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "GET or HEAD only", 405)
		return
	}
	file := "web/index.html"
	if req.URL.Path == "/view" || requestRole(req) == viewerRole {
		file = "web/view.html"
	}
	b, err := assets.ReadFile(file)
	if err != nil {
		http.Error(w, "Page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self'; frame-ancestors 'none'")
	if req.Method != "HEAD" {
		_, _ = w.Write(b)
	}
}
func (r *Runner) staticAsset(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "GET or HEAD only", 405)
		return
	}
	name := strings.TrimPrefix(req.URL.Path, "/assets/")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		http.NotFound(w, req)
		return
	}
	b, err := assets.ReadFile("web/assets/" + name)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	contentType := http.DetectContentType(b)
	if strings.HasSuffix(name, ".css") {
		contentType = "text/css; charset=utf-8"
	}
	if strings.HasSuffix(name, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	if req.Method != "HEAD" {
		_, _ = w.Write(b)
	}
}
func (r *Runner) status(w http.ResponseWriter, req *http.Request) {
	c := r.loadConfig()
	r.captureMu.RLock()
	ctl := r.captureController
	var live any
	if ctl != nil {
		live = ctl.Status()
	}
	r.captureMu.RUnlock()
	var lastTime string
	if t := r.lastPubSubCheck.Load(); t > 0 {
		lastTime = time.Unix(t, 0).Format(time.RFC3339)
	}
	reason, _ := r.lastIgnored.Load().(string)
	r.writeJSON(w, 200, map[string]any{
		"preRoll": live, "receivedEvents": r.eventCount.Load(), "lostRtpPackets": r.packetLoss.Load(),
		"lastReceiveCheck": lastTime, "lastIgnored": reason,
		"recorderEnabled":     r.recorderEnabled.Load(),
		"configured":          c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != "" && c.DeviceName != "",
		"pubsubReady":         c.Subscription != "" && c.ServiceAccountJSON != "",
		"capturing":           r.busy.Load(),
		"recorderTime":        time.Now().UTC().Format(time.RFC3339Nano),
		"eventDelivery":       r.delivery.snapshot(),
		"localMotion":         r.motion.snapshot(),
		"googleEventsEnabled": !c.GoogleEventsDisabled,
	})
}
func (r *Runner) config(w http.ResponseWriter, req *http.Request) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	if req.Method == "GET" {
		c := r.loadConfig()
		r.writeJSON(w, 200, map[string]any{
			"DeviceName": c.DeviceName, "Subscription": c.Subscription, "ClipSeconds": c.ClipSeconds, "DeviceAccessProject": c.DeviceAccessProject,
			"hasClientID": c.ClientID != "", "hasClientSecret": c.ClientSecret != "",
			"hasRefreshToken": c.RefreshToken != "", "hasServiceAccountJSON": c.ServiceAccountJSON != "",
		})
		return
	}
	if req.Method != "POST" || !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "JSON POST only", 405)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 32768)
	var update Configuration
	if err := json.NewDecoder(req.Body).Decode(&update); err != nil {
		r.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	c := r.loadConfig()
	old := c
	if update.ClipSeconds != 0 && (update.ClipSeconds < 5 || update.ClipSeconds > 600) {
		r.writeJSON(w, 400, map[string]string{"error": "Duration must be 5-600 seconds"})
		return
	}
	if update.ClientID != "" {
		c.ClientID = strings.TrimSpace(update.ClientID)
	}
	if update.ClientSecret != "" {
		c.ClientSecret = update.ClientSecret
	}
	if update.RefreshToken != "" {
		c.RefreshToken = update.RefreshToken
	}
	if update.DeviceName != "" {
		c.DeviceName = strings.TrimSpace(update.DeviceName)
	}
	if update.DeviceAccessProject != "" {
		c.DeviceAccessProject = strings.TrimSpace(update.DeviceAccessProject)
	}
	if update.Subscription != "" {
		c.Subscription = strings.TrimSpace(update.Subscription)
	}
	if update.ServiceAccountJSON != "" {
		var sa map[string]any
		if json.Unmarshal([]byte(update.ServiceAccountJSON), &sa) != nil {
			r.writeJSON(w, 400, map[string]string{"error": "Invalid service account JSON"})
			return
		}
		c.ServiceAccountJSON = update.ServiceAccountJSON
	}
	if update.ClipSeconds >= 5 && update.ClipSeconds <= 600 {
		c.ClipSeconds = update.ClipSeconds
	}
	if err := r.saveConfig(c); err != nil {
		r.writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	r.captureMu.RLock()
	ctl := r.captureController
	r.captureMu.RUnlock()
	if ctl != nil {
		_ = ctl.SetDuration(time.Duration(c.ClipSeconds) * time.Second)
	}
	if old.ClientID != c.ClientID || old.ClientSecret != c.ClientSecret || old.RefreshToken != c.RefreshToken || old.DeviceName != c.DeviceName {
		select {
		case r.configChanged <- struct{}{}:
		default:
		}
	}
	r.log("info", "Configuration saved locally")
	r.writeJSON(w, 200, map[string]string{"status": "saved"})
}
func (r *Runner) events(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	items := append([]Event{}, r.logs...)
	r.mu.Unlock()
	r.writeJSON(w, 200, items)
}

type ClipInfo struct {
	PreviewStatus   string
	PreviewError    string
	DurationSeconds float64
	EndReason       string
	Preview         string
	Name            string
	Size            int64
	Time            string
}

func (r *Runner) clipList() []ClipInfo {
	entries, _ := os.ReadDir(filepath.Join(r.dir, "clips"))
	out := make([]ClipInfo, 0)
	for _, item := range entries {
		if item.IsDir() || !(strings.HasSuffix(item.Name(), ".h264") || strings.HasSuffix(item.Name(), ".mp4")) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		preview := ""
		if _, err := os.Stat(filepath.Join(r.dir, "clips", item.Name()+".gif")); err == nil {
			preview = "/preview/" + item.Name() + ".gif"
		}
		ci := ClipInfo{Name: item.Name(), Size: info.Size(), Time: info.ModTime().Format(time.RFC3339), Preview: preview}
		if strings.HasSuffix(item.Name(), ".mp4") {
			ps := r.readPreviewState(item.Name())
			ci.PreviewStatus = ps.Status
			ci.PreviewError = ps.Message
			if ps.Status == "processing" && time.Since(ps.Updated) > 2*time.Minute {
				ci.PreviewStatus = "retrying"
				ci.PreviewError = "Worker delayed; retry queued"
			}
			if preview != "" {
				ci.PreviewStatus = "ready"
			}
			var meta capture.ClipMetadata
			if b, e := os.ReadFile(filepath.Join(r.dir, "clips", item.Name()+".meta.json")); e == nil {
				_ = json.Unmarshal(b, &meta)
			}
			ci.DurationSeconds = meta.DurationSeconds
			ci.EndReason = meta.EndReason
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}
func (r *Runner) clips(w http.ResponseWriter, req *http.Request) { r.writeJSON(w, 200, r.clipList()) }
func (r *Runner) clip(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	name := strings.TrimPrefix(req.URL.Path, "/clip/")
	if name != filepath.Base(name) || !(strings.HasSuffix(name, ".h264") || strings.HasSuffix(name, ".mp4")) || strings.Contains(name, "/") {
		http.Error(w, "Invalid clip", 400)
		return
	}
	if strings.HasSuffix(name, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	disposition := "inline"
	if req.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename=\""+name+"\"")
	http.ServeFile(w, req, filepath.Join(r.dir, "clips", name))
}
func (r *Runner) captureRequest(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !r.beginCapture("manual", time.Now()) {
		r.writeJSON(w, 409, map[string]string{"error": "Recorder paused or WebRTC stream not connected yet"})
		return
	}
	r.writeJSON(w, 202, map[string]string{"status": "started"})
}
func (r *Runner) beginCapture(source string, eventAt time.Time) bool {
	r.recordingMu.Lock()
	defer r.recordingMu.Unlock()
	if !r.recorderEnabled.Load() {
		return false
	}
	r.captureMu.RLock()
	ctl := r.captureController
	r.captureMu.RUnlock()
	if ctl != nil && ctl.Trigger(eventAt) {
		r.log("motion", "Trigger on configured Nest stream (account-specific camera ID ignored): "+source)
		return true
	}
	r.log("info", "Capture unavailable: waiting for persistent WebRTC stream")
	return false
}
func (r *Runner) testSDM(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	data, err := r.activateEvents(ctx, r.loadConfig(), true)
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}

	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	r.log("info", "Nest API authorized")
	r.writeJSON(w, 200, map[string]any{"status": "authorized", "deviceResponse": payload})
}
func accessToken(c Configuration) (string, error) {
	if c.ClientID == "" || c.ClientSecret == "" || c.RefreshToken == "" {
		return "", errors.New("OAuth client ID, client secret and refresh token required")
	}
	values := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "refresh_token": {c.RefreshToken}, "grant_type": {"refresh_token"}}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).PostForm("https://oauth2.googleapis.com/token", values)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&failure)
		if failure.Error == "invalid_grant" {
			return "", errors.New("Camera authorization expired or revoked (invalid_grant); reauthorize the same Google camera-owner account")
		}
		return "", fmt.Errorf("OAuth refresh HTTP %d: %s", resp.StatusCode, failure.Error)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&token); err != nil {
		return "", err
	}
	if token.AccessToken == "" {
		return "", errors.New("Empty OAuth access token")
	}
	return token.AccessToken, nil
}
func apiRequest(ctx context.Context, method, endpoint, token string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 35 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("Google API response exceeds 4 MiB; response not processed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Google API HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func pubSubToken(raw string) (string, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return "", err
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", errors.New("Invalid service account")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", errors.New("Invalid private key PEM")
	}
	var key *rsa.PrivateKey
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		key, _ = parsed.(*rsa.PrivateKey)
	} else {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if key == nil {
		return "", errors.New("Unsupported private key")
	}
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{"iss": sa.ClientEmail, "scope": "https://www.googleapis.com/auth/pubsub",
		"aud": "https://oauth2.googleapis.com/token", "iat": now, "exp": now + 3600})
	header := base64.RawURLEncoding.EncodeToString([]byte("{\"alg\":\"RS256\",\"typ\":\"JWT\"}"))
	value := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(value))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	assertion := value + "." + base64.RawURLEncoding.EncodeToString(signature)
	response, err := (&http.Client{Timeout: 20 * time.Second}).PostForm("https://oauth2.googleapis.com/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}})
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("Pub/Sub authorization HTTP %d", response.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", errors.New("Pub/Sub authorization returned an empty access token")
	}
	return result.AccessToken, nil
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (r *Runner) cleanupLoop(ctx context.Context) {
	for {
		r.cleanup()
		if !pause(ctx, time.Minute) {
			return
		}
	}
}

// cleanup expires completed clips and their sidecars using the clip timestamp.
// Compare in days to avoid duration overflow for large retention settings.
func (r *Runner) cleanup() {
	days := r.loadConfig().RetentionDays
	dir := filepath.Join(r.dir, "clips")
	files, _ := os.ReadDir(dir)
	now := time.Now()
	for _, file := range files {
		if file.IsDir() || !(strings.HasSuffix(file.Name(), ".mp4") || strings.HasSuffix(file.Name(), ".h264")) {
			continue
		}
		info, err := file.Info()
		if err != nil || now.Sub(info.ModTime()).Hours()/24 <= float64(days) {
			continue
		}
		name := filepath.Join(dir, file.Name())
		if err := os.Remove(name); err != nil {
			continue
		}
		for _, suffix := range []string{".gif", ".meta.json", ".preview.json"} {
			_ = os.Remove(name + suffix)
		}
	}
}
