package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func projectID(c Configuration) string {
	if c.DeviceAccessProject != "" {
		return c.DeviceAccessProject
	}
	if strings.HasPrefix(c.DeviceName, "enterprises/") {
		rest := strings.TrimPrefix(c.DeviceName, "enterprises/")
		return strings.Split(rest, "/")[0]
	}
	return ""
}
func (r *Runner) oauthLink(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	c := r.loadConfig()
	project := projectID(c)
	if project == "" || c.ClientID == "" {
		r.writeJSON(w, 400, map[string]string{"error": "Save OAuth client ID and Device Access project ID first"})
		return
	}
	// PCM authentication must open in the PHONE BROWSER, never embedded inside
	// Android WebView. The redirect URI must match Google Cloud OAuth config.
	endpoint := "https://nestservices.google.com/partnerconnections/" + url.PathEscape(project) + "/auth"
	q := url.Values{"redirect_uri": {"https://www.google.com"}, "access_type": {"offline"}, "prompt": {"consent"}, "client_id": {c.ClientID}, "response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/sdm.service"}}
	r.writeJSON(w, 200, map[string]string{"url": endpoint + "?" + q.Encode()})
}
func (r *Runner) oauthComplete(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 10000)
	var payload struct {
		RedirectURL string `json:"redirectUrl"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Invalid OAuth completion JSON"})
		return
	}
	var authCode string
	if parsed, err := url.Parse(strings.TrimSpace(payload.RedirectURL)); err == nil {
		authCode = parsed.Query().Get("code")
	}
	if authCode == "" {
		authCode = strings.TrimSpace(payload.RedirectURL)
	}
	if authCode == "" || len(authCode) > 4096 {
		r.writeJSON(w, 400, map[string]string{"error": "Paste the final redirected URL containing code= or the authorization code"})
		return
	}
	c := r.loadConfig()
	if c.ClientID == "" || c.ClientSecret == "" {
		r.writeJSON(w, 400, map[string]string{"error": "Client ID and secret required"})
		return
	}
	values := url.Values{"code": {authCode}, "client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "grant_type": {"authorization_code"}, "redirect_uri": {"https://www.google.com"}}
	response, err := (&http.Client{Timeout: 25 * time.Second}).PostForm("https://oauth2.googleapis.com/token", values)
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "OAuth network request failed"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		r.writeJSON(w, 400, map[string]string{"error": "OAuth code exchange rejected by Google"})
		return
	}
	var tokens struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&tokens); err != nil || tokens.RefreshToken == "" {
		r.writeJSON(w, 400, map[string]string{"error": "No refresh token in response; retry with prompt=consent"})
		return
	}
	c.RefreshToken = tokens.RefreshToken
	proj := projectID(c)
	if proj == "" {
		r.writeJSON(w, 400, map[string]string{"error": "Save Device Access project ID before authorizing"})
		return
	}
	listed, e := apiRequest(req.Context(), "GET", "https://smartdevicemanagement.googleapis.com/v1/enterprises/"+proj+"/devices", tokens.AccessToken, nil)
	if e != nil {
		r.writeJSON(w, 400, map[string]string{"error": "OAuth exchanged but devices.list failed: " + e.Error()})
		return
	}
	var devices struct {
		Devices []struct {
			Name   string                     `json:"name"`
			Traits map[string]json.RawMessage `json:"traits"`
		} `json:"devices"`
	}
	if e = json.Unmarshal(listed, &devices); e != nil {
		r.writeJSON(w, 500, map[string]string{"error": "Google device list decode failed"})
		return
	}
	if c.DeviceName == "" {
		for _, d := range devices.Devices {
			if _, ok := d.Traits["sdm.devices.traits.CameraLiveStream"]; ok {
				c.DeviceName = d.Name
				break
			}
		}
	}
	file, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		r.writeJSON(w, 500, map[string]string{"error": "Configuration serialization failed"})
		return
	}
	if err = os.WriteFile(r.cfgPath(), file, 0600); err != nil {
		r.writeJSON(w, 500, map[string]string{"error": "Local save failed"})
		return
	}
	select {
	case r.configChanged <- struct{}{}:
	default:
	}
	r.log("info", "OAuth completed and devices.list called (activates events)")
	r.writeJSON(w, 200, map[string]any{"status": "authorized", "deviceCount": len(devices.Devices), "deviceName": c.DeviceName})
}
func (r *Runner) testSubscription(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	c := r.loadConfig()
	if !strings.HasPrefix(c.Subscription, "projects/") || c.ServiceAccountJSON == "" {
		r.writeJSON(w, 400, map[string]string{"error": "Configure Pub/Sub subscription and service account first"})
		return
	}
	token, err := pubSubToken(c.ServiceAccountJSON)
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	metadata, err := apiRequest(req.Context(), "GET", "https://pubsub.googleapis.com/v1/"+c.Subscription, token, nil)
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var sub struct {
		Topic  string `json:"topic"`
		Filter string `json:"filter"`
		Push   struct {
			Endpoint string `json:"pushEndpoint"`
		} `json:"pushConfig"`
		BigQuery struct {
			Table string `json:"table"`
		} `json:"bigqueryConfig"`
		Storage struct {
			Bucket string `json:"bucket"`
		} `json:"cloudStorageConfig"`
	}
	if err = json.Unmarshal(metadata, &sub); err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Invalid subscription metadata"})
		return
	}
	r.delivery.update(func(s *eventDeliveryStatus) { s.Topic = sub.Topic; s.Filter = sub.Filter })
	if sub.Push.Endpoint != "" || sub.BigQuery.Table != "" || sub.Storage.Bucket != "" {
		r.writeJSON(w, 400, map[string]string{"error": "This is not a pull subscription. CamSaver needs a dedicated pull subscription on the Nest events topic."})
		return
	}
	permissionData, err := apiRequest(req.Context(), "POST", "https://pubsub.googleapis.com/v1/"+c.Subscription+":testIamPermissions", token, map[string]any{"permissions": []string{"pubsub.subscriptions.consume"}})
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Receive-permission check failed: " + err.Error()})
		return
	}
	var permissions struct {
		Permissions []string `json:"permissions"`
	}
	if err = json.Unmarshal(permissionData, &permissions); err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Invalid receive-permission response"})
		return
	}
	allowed := false
	for _, permission := range permissions.Permissions {
		if permission == "pubsub.subscriptions.consume" {
			allowed = true
		}
	}
	if !allowed {
		r.writeJSON(w, 403, map[string]string{"error": "The service account can read subscription details but cannot receive messages. Grant Pub/Sub Subscriber on this subscription."})
		return
	}
	note := "Receive permission verified. Compare this topic with Device Access Console; this check does not prove Nest is publishing messages."
	if sub.Filter != "" {
		note += " A subscription filter is active and may discard events."
	}
	r.writeJSON(w, 200, map[string]any{"status": "permissions verified", "note": note, "topic": sub.Topic, "filter": sub.Filter})
}
