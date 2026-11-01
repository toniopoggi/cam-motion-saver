package engine

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionSettingsPersistAndRejectInvalid(t *testing.T) {
	r := testRunner(t)
	if r.loadConfig().RetentionDays != 1 {
		t.Fatal("existing installs must default to one day")
	}
	post := func(body string) int {
		req := httptest.NewRequest("POST", "/api/recording/settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.recordingSettings(w, req)
		return w.Code
	}
	if post(`{"RetentionDays":30}`) != 200 {
		t.Fatal("save failed")
	}
	if c := r.loadConfig(); c.RetentionDays != 30 || c.ClipSeconds != 60 {
		t.Fatalf("settings lost: %+v", c)
	}
	if post(`{"ClipSeconds":120}`) != 200 || r.loadConfig().RetentionDays != 30 {
		t.Fatal("duration save overwrote retention")
	}
	for _, body := range []string{`{"RetentionDays":0}`, `{"RetentionDays":-1}`, `{"RetentionDays":1.5}`, `{"RetentionDays":"7"}`, `{}`, `{"RetentionDays":9007199254740992}`} {
		if post(body) != 400 {
			t.Fatalf("accepted %s", body)
		}
	}
	if r.loadConfig().RetentionDays != 30 {
		t.Fatal("invalid request changed configuration")
	}
	if post(`{"RetentionDays":9007199254740991}`) != 200 {
		t.Fatal("large safe value rejected")
	}
	r.cleanup() // Must not overflow a time.Duration.
}

func TestRetentionCleanupUsesSelectedDaysAndKeepsLargeFiles(t *testing.T) {
	r := testRunner(t)
	create := func(name string, age time.Duration) string {
		p := filepath.Join(r.dir, "clips", name)
		if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exists := func(p string) bool { _, e := os.Stat(p); return e == nil }
	old := create("old.mp4", 8*24*time.Hour)
	for _, suffix := range []string{".gif", ".meta.json", ".preview.json"} {
		create("old.mp4"+suffix, 0)
	}
	week := create("week.mp4", 6*24*time.Hour)
	recent := create("recent.mp4", 23*time.Hour)
	partial := create("active.mp4.part", 40*24*time.Hour)
	c := r.loadConfig()
	c.RetentionDays = 7
	if err := r.saveConfig(c); err != nil {
		t.Fatal(err)
	}
	r.cleanup()
	if exists(old) || exists(old+".gif") || exists(old+".meta.json") || exists(old+".preview.json") {
		t.Fatal("expired clip or sidecar retained")
	}
	if !exists(week) || !exists(recent) || !exists(partial) {
		t.Fatal("retained or active clip removed")
	}
	f, err := os.OpenFile(recent, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(3 * 1024 * 1024 * 1024)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r.cleanup()
	if !exists(recent) {
		t.Fatal("hidden size cap removed recent recording")
	}
	c.RetentionDays = 1
	r.saveConfig(c)
	r.cleanup()
	if exists(week) || !exists(recent) {
		t.Fatal("one-day retention not applied")
	}
}
