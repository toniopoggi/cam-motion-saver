package engine

import (
	"camfreenest/motionevents"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type eventDeliveryStatus struct {
	Attempts        uint64 `json:"pullAttempts"`
	Successes       uint64 `json:"successfulPulls"`
	Empty           uint64 `json:"emptyPulls"`
	Messages        uint64 `json:"messagesReceived"`
	Rejected        uint64 `json:"messagesRejected"`
	Duplicates      uint64 `json:"duplicates"`
	CaptureDeferred uint64 `json:"captureDeferred"`
	LastSuccess     string `json:"lastSuccessfulPull"`
	LastMessage     string `json:"lastMessage"`
	LastError       string `json:"lastError"`
	ErrorStage      string `json:"errorStage"`
	LastActivation  string `json:"lastEventActivation"`
	ActivationError string `json:"activationError"`
	Topic           string `json:"topic"`
	Filter          string `json:"subscriptionFilter"`
}
type eventDelivery struct {
	mu           sync.Mutex
	value        eventDeliveryStatus
	activationMu sync.Mutex
	activated    [32]byte
	sessionMu    sync.Mutex
	cancel       context.CancelFunc
}

func (d *eventDelivery) update(f func(*eventDeliveryStatus)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(&d.value)
}
func (d *eventDelivery) snapshot() eventDeliveryStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.value
}
func nowText() string { return time.Now().UTC().Format(time.RFC3339) }
func activationKey(c Configuration) [32]byte {
	b, _ := json.Marshal([]string{c.ClientID, c.ClientSecret, c.RefreshToken, projectID(c), c.Subscription})
	return sha256.Sum256(b)
}
func (r *Runner) activateEvents(ctx context.Context, c Configuration, force bool) ([]byte, error) {
	r.delivery.activationMu.Lock()
	defer r.delivery.activationMu.Unlock()
	key := activationKey(c)
	if !force && r.delivery.activated == key {
		return nil, nil
	}
	fail := func(e error) ([]byte, error) {
		r.delivery.update(func(s *eventDeliveryStatus) { s.ActivationError = e.Error() })
		return nil, e
	}
	if !validCamera(c) || projectID(c) == "" {
		return fail(errors.New("Configure camera authorization before activating events"))
	}
	token, e := accessToken(c)
	if e != nil {
		return fail(e)
	}
	data, e := apiRequest(ctx, "GET", "https://smartdevicemanagement.googleapis.com/v1/enterprises/"+projectID(c)+"/devices", token, nil)
	if e != nil {
		return fail(e)
	}
	var list struct {
		Devices []json.RawMessage `json:"devices"`
	}
	if e = json.Unmarshal(data, &list); e != nil {
		return fail(fmt.Errorf("Event activation response: %w", e))
	}
	if len(list.Devices) == 0 {
		return fail(errors.New("Google returned no authorized devices; check the account and camera permissions in Partner Connections Manager"))
	}
	r.delivery.activated = key
	r.delivery.update(func(s *eventDeliveryStatus) { s.LastActivation = nowText(); s.ActivationError = "" })
	r.log("info", "Nest devices.list succeeded: event activation requested for current authorization")
	return data, nil
}
func (r *Runner) repairEvents(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	if _, e := r.activateEvents(ctx, r.loadConfig(), true); e != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Event activation failed: " + e.Error()})
		return
	}
	r.writeJSON(w, 200, map[string]string{"status": "activation requested", "note": "Google accepted devices.list. This does not prove event publication; inspect message receipt and pull status below."})
}
func (r *Runner) cancelEventPull() {
	r.delivery.sessionMu.Lock()
	defer r.delivery.sessionMu.Unlock()
	if r.delivery.cancel != nil {
		r.delivery.cancel()
	}
}

var errRecorderPaused = errors.New("recorder paused")

func (r *Runner) beginEventPull(parent context.Context) (context.Context, func(), error) {
	r.delivery.sessionMu.Lock()
	defer r.delivery.sessionMu.Unlock()
	if !r.recorderEnabled.Load() {
		return nil, nil, errRecorderPaused
	}
	ctx, cancel := context.WithCancel(parent)
	r.delivery.cancel = cancel
	return ctx, func() { cancel(); r.delivery.sessionMu.Lock(); r.delivery.cancel = nil; r.delivery.sessionMu.Unlock() }, nil
}
func (r *Runner) deliveryError(stage string, e error) {
	r.delivery.update(func(s *eventDeliveryStatus) { s.ErrorStage = stage; s.LastError = e.Error() })
	r.log("error", "Pub/Sub "+stage+": "+e.Error())
}

type pulledMessage struct {
	AckID   string `json:"ackId"`
	Message struct {
		Data string `json:"data"`
		ID   string `json:"messageId"`
	} `json:"message"`
}

func (r *Runner) pullEvents(parent context.Context, c Configuration, token string, seen map[string]time.Time) error {
	ctx, finish, e := r.beginEventPull(parent)
	if e != nil {
		return e
	}
	defer finish()
	r.delivery.update(func(s *eventDeliveryStatus) { s.Attempts++ })
	r.lastPubSubCheck.Store(time.Now().Unix())
	data, e := apiRequest(ctx, "POST", "https://pubsub.googleapis.com/v1/"+c.Subscription+":pull", token, map[string]any{"maxMessages": 10})
	if e != nil {
		if !r.recorderEnabled.Load() || parent.Err() != nil || r.loadConfig().GoogleEventsDisabled {
			return errRecorderPaused
		}
		r.deliveryError("pull", e)
		return e
	}
	var response struct {
		Messages []pulledMessage `json:"receivedMessages"`
	}
	if e = json.Unmarshal(data, &response); e != nil {
		e = fmt.Errorf("invalid pull response JSON: %w", e)
		r.deliveryError("decode", e)
		return e
	}
	r.delivery.update(func(s *eventDeliveryStatus) {
		s.Successes++
		s.LastSuccess = nowText()
		s.LastError = ""
		s.ErrorStage = ""
		s.Messages += uint64(len(response.Messages))
		if len(response.Messages) == 0 {
			s.Empty++
		} else {
			s.LastMessage = nowText()
		}
	})
	acks, release := []string{}, []string{}
	for _, item := range response.Messages {
		if item.AckID == "" {
			r.deliveryError("decode", errors.New("message has no acknowledgment ID"))
			continue
		}
		if !r.recorderEnabled.Load() {
			release = append(release, item.AckID)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(item.Message.Data)
		if err != nil {
			r.rejectMessage("Invalid base64 message data")
			acks = append(acks, item.AckID)
			continue
		}
		event, err := motionevents.Parse(raw, item.Message.ID, time.Now())
		if err != nil {
			reason := err.Error()
			if reason == "motion event outside freshness window" {
				var timing motionevents.Message
				if json.Unmarshal(raw, &timing) == nil {
					reason += " (event time " + timing.Timestamp + "; recorder time " + nowText() + ")"
				}
			}
			r.rejectMessage(reason)
			acks = append(acks, item.AckID)
			continue
		}
		if _, exists := seen[event.ID]; exists {
			r.delivery.update(func(s *eventDeliveryStatus) { s.Duplicates++ })
			r.lastIgnored.Store("Duplicate event")
			acks = append(acks, item.AckID)
			continue
		}
		if !r.beginCapture("pubsub", event.At) {
			r.delivery.update(func(s *eventDeliveryStatus) { s.CaptureDeferred++ })
			release = append(release, item.AckID)
			continue
		}
		seen[event.ID] = time.Now()
		r.eventCount.Add(1)
		r.log("motion", "SDM camera event ("+event.Kind+") at "+event.At.Format(time.RFC3339))
		acks = append(acks, item.AckID)
	}
	cleanupCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if len(release) > 0 {
		_, e = apiRequest(cleanupCtx, "POST", "https://pubsub.googleapis.com/v1/"+c.Subscription+":modifyAckDeadline", token, map[string]any{"ackIds": release, "ackDeadlineSeconds": 0})
		if e != nil {
			r.deliveryError("release", e)
			return e
		}
	}
	if len(acks) > 0 {
		_, e = apiRequest(cleanupCtx, "POST", "https://pubsub.googleapis.com/v1/"+c.Subscription+":acknowledge", token, map[string]any{"ackIds": acks})
		if e != nil {
			r.deliveryError("acknowledgment", e)
			return e
		}
	}
	return nil
}
func (r *Runner) rejectMessage(reason string) {
	r.delivery.update(func(s *eventDeliveryStatus) { s.Rejected++ })
	r.lastIgnored.Store(reason)
	r.log("info", "Pub/Sub message ignored: "+reason)
}
func (r *Runner) pollLoop(ctx context.Context) {
	seen := make(map[string]time.Time)
	var cachedFor, cachedToken string
	var tokenUntil, activateAfter time.Time
	var lastCredentialKey [32]byte
	for ctx.Err() == nil {
		if !r.recorderEnabled.Load() {
			if !pause(ctx, time.Second) {
				return
			}
			continue
		}
		c := r.loadConfig()
		if c.GoogleEventsDisabled || c.Subscription == "" || c.ServiceAccountJSON == "" {
			if !pause(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if !strings.HasPrefix(c.Subscription, "projects/") || !strings.Contains(c.Subscription, "/subscriptions/") {
			r.deliveryError("configuration", errors.New("Invalid Pub/Sub subscription"))
			if !pause(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if key := activationKey(c); key != lastCredentialKey {
			activateAfter = time.Time{}
			lastCredentialKey = key
		}
		if time.Now().After(activateAfter) {
			activationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, e := r.activateEvents(activationCtx, c, false); e != nil {
				r.log("error", "Event activation: "+e.Error())
			}
			cancel()
			activateAfter = time.Now().Add(5 * time.Second)
			if r.delivery.snapshot().ActivationError != "" {
				activateAfter = time.Now().Add(60 * time.Second)
			}
		}
		if cachedFor != c.ServiceAccountJSON || time.Now().After(tokenUntil) {
			token, e := pubSubToken(c.ServiceAccountJSON)
			if e != nil {
				r.deliveryError("authorization", e)
				if !pause(ctx, 30*time.Second) {
					return
				}
				continue
			}
			cachedFor, cachedToken, tokenUntil = c.ServiceAccountJSON, token, time.Now().Add(50*time.Minute)
		}
		e := r.pullEvents(ctx, c, cachedToken, seen)
		for id, at := range seen {
			if time.Since(at) > 24*time.Hour {
				delete(seen, id)
			}
		}
		wait := 4 * time.Second
		if e != nil && !errors.Is(e, errRecorderPaused) {
			wait = 15 * time.Second
			tokenUntil = time.Time{}
		}
		if !pause(ctx, wait) {
			return
		}
	}
}
