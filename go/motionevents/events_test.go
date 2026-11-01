package motionevents

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDifferentGoogleAccountCameraIDStillTriggers(t *testing.T) {
	now := time.Now().UTC()
	data := fmt.Sprintf(`{"eventId":"abc","timestamp":"%s","resourceUpdate":{"name":"enterprises/P/devices/DIFFERENT_ACCOUNT_CAMERA_ID","events":{"sdm.devices.events.CameraMotion.Motion":{}}}}`, now.Format(time.RFC3339Nano))
	got, err := Parse([]byte(data), "pubsub-2", now)
	if err != nil || got.ID != "abc" || !strings.Contains(got.DeviceName, "DIFFERENT_ACCOUNT_CAMERA_ID") {
		t.Fatalf("unexpected trigger: %+v %v", got, err)
	}
}
func TestIgnoresPubSubProbe(t *testing.T) {
	if _, err := Parse([]byte(`{"test":true}`), "probe", time.Now()); err == nil {
		t.Fatal("probe triggered recording")
	}
}
func TestOtherDeviceTraitsDoNotTrigger(t *testing.T) {
	msg := `{"eventId":"id","resourceUpdate":{"name":"enterprises/P/devices/X","events":{"sdm.devices.traits.CameraLiveStream":{"state":"ok"}}}}`
	if _, err := Parse([]byte(msg), "abc", time.Now()); err == nil {
		t.Fatal("non-event triggered")
	}
}
func TestStaleEventRejected(t *testing.T) {
	now := time.Now().UTC()
	msg := fmt.Sprintf(`{"eventId":"id","timestamp":"%s","resourceUpdate":{"name":"enterprises/P/devices/X","events":{"sdm.devices.events.CameraPerson.Person":{}}}}`, now.Add(-3*time.Minute).Format(time.RFC3339Nano))
	if _, err := Parse([]byte(msg), "id", now); err == nil {
		t.Fatal("stale message triggered")
	}
}
func TestMissingTimestampUsesArrival(t *testing.T) {
	now := time.Now().UTC()
	msg := `{"resourceUpdate":{"name":"enterprises/P/devices/X","events":{"sdm.devices.events.CameraMotion.Motion":{}}}}`
	got, err := Parse([]byte(msg), "pubsub123", now)
	if err != nil || !got.At.Equal(now) || got.ID != "pubsub123" {
		t.Fatalf("%+v %v", got, err)
	}
}
