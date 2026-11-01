package engine

import (
	"camfreenest/capture"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func mockGoogle(t *testing.T, f roundTripFunc) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
}
func reply(code int, text string) (*http.Response, error) {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
}
func cameraConfiguration() Configuration {
	return Configuration{ClientID: "id", ClientSecret: "private-client-secret", RefreshToken: "private-refresh-token", DeviceAccessProject: "test", DeviceName: "enterprises/test/devices/camera", Subscription: "projects/test/subscriptions/events", ClipSeconds: 30}
}
func eventMessage(t *testing.T, id string) pulledMessage {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"eventId": id, "timestamp": time.Now().Format(time.RFC3339Nano), "resourceUpdate": map[string]any{"name": "enterprises/other-account/devices/another-id", "events": map[string]any{"sdm.devices.events.CameraMotion.Motion": map[string]any{}}}})
	var m pulledMessage
	m.AckID = "ack-" + id
	m.Message.ID = id
	m.Message.Data = base64.StdEncoding.EncodeToString(b)
	return m
}
func pullResponse(ms ...pulledMessage) string {
	b, _ := json.Marshal(map[string]any{"receivedMessages": ms})
	return string(b)
}
func readyController(t *testing.T, r *Runner) *capture.Controller {
	t.Helper()
	ctl, e := capture.New(filepath.Join(r.dir, "clips"), 5*time.Second, 30*time.Second, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctl.BeginEpoch(1)
	r.captureController = ctl
	t.Cleanup(ctl.Close)
	return ctl
}
func TestActivationRunsAgainAfterAuthorizationChanges(t *testing.T) {
	r := testRunner(t)
	c := cameraConfiguration()
	lists, tokens := 0, 0
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "oauth2.googleapis.com" {
			tokens++
			return reply(200, `{"access_token":"private-access-token"}`)
		}
		lists++
		return reply(200, `{"devices":[{"name":"camera"}]}`)
	})
	for i := 0; i < 2; i++ {
		if _, e := r.activateEvents(context.Background(), c, false); e != nil {
			t.Fatal(e)
		}
	}
	if lists != 1 || tokens != 1 {
		t.Fatal("activation not cached")
	}
	c.RefreshToken = "new-private-refresh-token"
	if _, e := r.activateEvents(context.Background(), c, false); e != nil {
		t.Fatal(e)
	}
	if lists != 2 || tokens != 2 || r.delivery.snapshot().LastActivation == "" {
		t.Fatal("new authorization not activated")
	}
	b, _ := json.Marshal(r.delivery.snapshot())
	if strings.Contains(string(b), "private-") {
		t.Fatal("credential leaked")
	}
}
func TestRawReceiptAndRejectionsVisibleWithoutRecording(t *testing.T) {
	r := testRunner(t)
	valid := eventMessage(t, "fresh")
	invalid := pulledMessage{AckID: "bad"}
	invalid.Message.Data = "bad%%%"
	var acked, released []string
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, ":pull"):
			return reply(200, pullResponse(valid, invalid))
		case strings.HasSuffix(req.URL.Path, ":acknowledge"):
			var b struct {
				IDs []string `json:"ackIds"`
			}
			json.NewDecoder(req.Body).Decode(&b)
			acked = b.IDs
		case strings.HasSuffix(req.URL.Path, ":modifyAckDeadline"):
			var b struct {
				IDs     []string `json:"ackIds"`
				Seconds int      `json:"ackDeadlineSeconds"`
			}
			json.NewDecoder(req.Body).Decode(&b)
			released = b.IDs
			if b.Seconds != 0 {
				t.Fatal("lease not released")
			}
		default:
			t.Fatal(req.URL)
		}
		return reply(200, `{}`)
	})
	seen := map[string]time.Time{}
	if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", seen); e != nil {
		t.Fatal(e)
	}
	s := r.delivery.snapshot()
	if s.Messages != 2 || s.Rejected != 1 || s.CaptureDeferred != 1 || s.Successes != 1 || s.LastMessage == "" {
		t.Fatalf("bad counters %+v", s)
	}
	if len(acked) != 1 || acked[0] != "bad" || len(released) != 1 || released[0] != valid.AckID || len(seen) != 0 {
		t.Fatal("motion consumed before stream ready")
	}
}
func TestAccountIndependentEventTriggersAndDeduplicates(t *testing.T) {
	r := testRunner(t)
	ctl := readyController(t, r)
	m := eventMessage(t, "event")
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ":pull") {
			return reply(200, pullResponse(m))
		}
		return reply(200, `{}`)
	})
	seen := map[string]time.Time{}
	for i := 0; i < 2; i++ {
		if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", seen); e != nil {
			t.Fatal(e)
		}
	}
	if r.eventCount.Load() != 1 || !ctl.Status().Pending || r.delivery.snapshot().Duplicates != 1 {
		t.Fatal("account IDs or deduplication broke event")
	}
}
func TestMalformedPullAndHTTPFailuresAreVisible(t *testing.T) {
	for _, body := range []string{"{", strings.Repeat("x", (4<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			r := testRunner(t)
			mockGoogle(t, func(req *http.Request) (*http.Response, error) { return reply(200, body) })
			if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}); e == nil {
				t.Fatal("bad response accepted")
			}
			s := r.delivery.snapshot()
			if s.LastError == "" || s.Successes != 0 || s.Messages != 0 {
				t.Fatal("silent failure")
			}
		})
	}
}
func TestPausedRecorderDoesNotPullAndCancelsInflightRequest(t *testing.T) {
	r := testRunner(t)
	calls := 0
	mockGoogle(t, func(req *http.Request) (*http.Response, error) { calls++; return reply(200, `{}`) })
	r.recorderEnabled.Store(false)
	if !errors.Is(r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}), errRecorderPaused) || calls != 0 {
		t.Fatal("paused polling")
	}
	r.recorderEnabled.Store(true)
	ctx, finish, e := r.beginEventPull(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer finish()
	w := httptest.NewRecorder()
	r.stopRecorder(w, httptest.NewRequest("POST", "http://127.0.0.1:8767/api/recorder/stop", nil))
	if w.Code != 200 || ctx.Err() != context.Canceled {
		t.Fatal("stop did not cancel")
	}
}
func TestStopRacingPullReleasesMessagesWithoutAcknowledgment(t *testing.T) {
	r := testRunner(t)
	acks, releases := 0, 0
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ":pull") {
			r.recorderEnabled.Store(false)
			return reply(200, pullResponse(eventMessage(t, "raced")))
		}
		if strings.HasSuffix(req.URL.Path, ":acknowledge") {
			acks++
		}
		if strings.HasSuffix(req.URL.Path, ":modifyAckDeadline") {
			releases++
		}
		return reply(200, `{}`)
	})
	if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}); e != nil {
		t.Fatal(e)
	}
	if acks != 0 || releases != 1 || r.eventCount.Load() != 0 {
		t.Fatal("stopped recorder consumed event")
	}
}
func TestAcknowledgmentErrorsAndEmptyPullsHaveSeparateStatus(t *testing.T) {
	r := testRunner(t)
	readyController(t, r)
	empty := false
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ":pull") {
			if empty {
				return reply(200, `{}`)
			}
			return reply(200, pullResponse(eventMessage(t, "ack-error")))
		}
		return reply(403, `{"error":"denied"}`)
	})
	if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}); e == nil {
		t.Fatal("ack error hidden")
	}
	if r.delivery.snapshot().ErrorStage != "acknowledgment" {
		t.Fatal("wrong stage")
	}
	empty = true
	if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}); e != nil {
		t.Fatal(e)
	}
	s := r.delivery.snapshot()
	if s.Empty != 1 || s.LastSuccess == "" || s.LastError != "" {
		t.Fatal("empty pull marked broken")
	}
}
func serviceAccountFixture(t *testing.T) string {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	b, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]string{"client_email": "test@example.invalid", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))})
	return string(raw)
}
func TestSubscriptionCheckRequiresReceivePermissionAndShowsTopic(t *testing.T) {
	r := testRunner(t)
	c := cameraConfiguration()
	c.ServiceAccountJSON = serviceAccountFixture(t)
	r.saveConfig(c)
	allowed := false
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "oauth2.googleapis.com" {
			return reply(200, `{"access_token":"token"}`)
		}
		if strings.HasSuffix(req.URL.Path, ":testIamPermissions") {
			if allowed {
				return reply(200, `{"permissions":["pubsub.subscriptions.consume"]}`)
			}
			return reply(200, `{}`)
		}
		return reply(200, `{"topic":"projects/test/topics/nest","filter":"attributes.kind=\"camera\""}`)
	})
	for _, expected := range []int{403, 200} {
		w := httptest.NewRecorder()
		r.testSubscription(w, httptest.NewRequest("POST", "http://127.0.0.1:8767/api/subscription/test", nil))
		if w.Code != expected {
			t.Fatalf("expected %d got %d: %s", expected, w.Code, w.Body.String())
		}
		allowed = true
	}
	if r.delivery.snapshot().Topic != "projects/test/topics/nest" || r.delivery.snapshot().Filter == "" {
		t.Fatal("missing metadata")
	}
}
func TestRemotePairingDoesNotGrantEventRepair(t *testing.T) {
	r := testRunner(t)
	req := remoteRequest("POST", "/api/events/repair")
	req.AddCookie(&http.Cookie{Name: "camfreenest_admin", Value: r.adminToken})
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { t.Fatal("remote repair granted") })).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestActivationFailureDoesNotSuppressNextAttempt(t *testing.T) {
	r := testRunner(t)
	c := cameraConfiguration()
	expired := true
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "oauth2.googleapis.com" {
			if expired {
				return reply(400, `{"error":"invalid_grant"}`)
			}
			return reply(200, `{"access_token":"token"}`)
		}
		return reply(200, `{"devices":[{"name":"camera"}]}`)
	})
	if _, e := r.activateEvents(context.Background(), c, false); e == nil || !strings.Contains(e.Error(), "expired or revoked") {
		t.Fatal("expiry not explained")
	}
	if r.delivery.snapshot().ActivationError == "" || r.delivery.snapshot().LastActivation != "" {
		t.Fatal("false activation success")
	}
	expired = false
	if _, e := r.activateEvents(context.Background(), c, false); e != nil {
		t.Fatal(e)
	}
	if r.delivery.snapshot().ActivationError != "" || r.delivery.snapshot().LastActivation == "" {
		t.Fatal("activation not retried")
	}
}
func TestValidPullBeyondOld64KiBLimitIsProcessed(t *testing.T) {
	r := testRunner(t)
	readyController(t, r)
	m := eventMessage(t, "large")
	raw, _ := base64.StdEncoding.DecodeString(m.Message.Data)
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	payload["padding"] = strings.Repeat("x", 100000)
	raw, _ = json.Marshal(payload)
	m.Message.Data = base64.StdEncoding.EncodeToString(raw)
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ":pull") {
			return reply(200, pullResponse(m))
		}
		return reply(200, `{}`)
	})
	if e := r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{}); e != nil {
		t.Fatal(e)
	}
	if r.eventCount.Load() != 1 {
		t.Fatal("large response event lost")
	}
}
func TestStopCancelsBlockedHTTPPullWithoutErrorAlert(t *testing.T) {
	r := testRunner(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	mockGoogle(t, func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	go func() {
		done <- r.pullEvents(context.Background(), cameraConfiguration(), "token", map[string]time.Time{})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pull not started")
	}
	w := httptest.NewRecorder()
	r.stopRecorder(w, httptest.NewRequest("POST", "http://127.0.0.1:8767/api/recorder/stop", nil))
	select {
	case e := <-done:
		if !errors.Is(e, errRecorderPaused) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("pull not canceled")
	}
	if r.delivery.snapshot().LastError != "" {
		t.Fatal("pause shown as error")
	}
}
