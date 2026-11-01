package engine

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type previewState struct {
	Status   string    `json:"status"`
	Message  string    `json:"message"`
	Attempts int       `json:"attempts"`
	Updated  time.Time `json:"updated"`
	RetryAt  time.Time `json:"retryAt"`
}

func validClipName(name string) bool {
	return name == filepath.Base(name) && strings.HasSuffix(name, ".mp4") && !strings.ContainsAny(name, "/\\")
}
func (r *Runner) readPreviewState(name string) previewState {
	s := previewState{Status: "queued"}
	if b, e := os.ReadFile(filepath.Join(r.dir, "clips", name+".preview.json")); e == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}
func (r *Runner) storePreviewState(name string, s previewState) error {
	if !validClipName(name) {
		return errors.New("invalid clip name")
	}
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	p := filepath.Join(r.dir, "clips", name+".preview.json")
	if e = os.WriteFile(p+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(p+".tmp", p)
}
func activeRunner() *Runner { stateMu.Lock(); defer stateMu.Unlock(); return state }

// ClaimPreviewJobs chooses eligible work BEFORE imposing the six-job batch cap.
// A broken recent clip must not starve all older previews.
func ClaimPreviewJobs() string {
	r := activeRunner()
	if r == nil {
		return "[]"
	}
	jobs := r.claimPreviewJobs(time.Now())
	b, _ := json.Marshal(jobs)
	return string(b)
}
func (r *Runner) claimPreviewJobs(now time.Time) []string {
	r.previewMu.Lock()
	defer r.previewMu.Unlock()
	jobs := []string{}
	for _, clip := range r.clipList() {
		if !validClipName(clip.Name) || clip.Preview != "" {
			continue
		}
		s := r.readPreviewState(clip.Name)
		if s.Status == "failed" || now.Before(s.RetryAt) || (s.Status == "processing" && now.Sub(s.Updated) < 2*time.Minute) {
			continue
		}
		if s.Attempts >= 3 {
			s.Status = "failed"
			s.Message = "Preview worker did not finish. Retry the preview."
			_ = r.storePreviewState(clip.Name, s)
			continue
		}
		s.Status = "processing"
		s.Message = ""
		s.Attempts++
		s.Updated = now
		s.RetryAt = time.Time{}
		if e := r.storePreviewState(clip.Name, s); e != nil {
			r.log("error", "Preview state: "+e.Error())
			continue
		}
		jobs = append(jobs, clip.Name)
		if len(jobs) == 6 {
			break
		}
	}
	return jobs
}

// PreviewFailed reports decoder errors to the gallery and retries at most three
// times; users can then explicitly retry instead of seeing an endless spinner.
func PreviewFailed(name, message string) string {
	r := activeRunner()
	if r == nil {
		return "Service not running"
	}
	if !validClipName(name) {
		return "Invalid clip name"
	}
	r.previewMu.Lock()
	defer r.previewMu.Unlock()
	s := r.readPreviewState(name)
	if s.Attempts < 1 {
		s.Attempts = 1
	}
	s.Updated = time.Now()
	if len(message) > 240 {
		message = message[:240]
	}
	s.Message = message
	s.Status = "retrying"
	s.RetryAt = s.Updated.Add(time.Duration(s.Attempts) * 20 * time.Second)
	if s.Attempts >= 3 {
		s.Status = "failed"
	}
	if e := r.storePreviewState(name, s); e != nil {
		return e.Error()
	}
	r.log("error", "Preview "+name+": "+message)
	return ""
}
func (r *Runner) retryPreview(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var in struct{ Name string }
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&in) != nil || !validClipName(in.Name) {
		http.Error(w, "Invalid clip name", 400)
		return
	}
	if _, e := os.Stat(filepath.Join(r.dir, "clips", in.Name)); e != nil {
		http.NotFound(w, req)
		return
	}
	r.previewMu.Lock()
	e := r.storePreviewState(in.Name, previewState{Status: "queued", Updated: time.Now()})
	r.previewMu.Unlock()
	if e != nil {
		r.writeJSON(w, 500, map[string]string{"error": e.Error()})
		return
	}
	r.writeJSON(w, 200, map[string]string{"status": "queued"})
}
