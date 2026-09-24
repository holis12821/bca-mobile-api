package redis

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// The cache used to marshal onboarding.Session directly. That struct is shaped
// for API responses, where device_id, the internal id and the TNC version are
// all `json:"-"` — so every field the audit trail depends on was silently
// dropped on the way into Redis and came back empty. This test pins the round
// trip down to the field.
func TestCachedSession_RoundTripKeepsEveryField(t *testing.T) {
	deleted := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	original := &onboarding.Session{
		ID:          uuid.New(),
		SessionID:   "onb_9f8e7d6c5b4a1122",
		DeviceID:    "device-nurholis-001",
		ProductType: onboarding.ProductTahapanXpre,
		CurrentStep: onboarding.StepBiometric,
		TNCVersion:  "2026-09-01",
		StepsCompleted: onboarding.StepsCompleted{
			TNCAccepted:       true,
			OCRVerified:       true,
			PersonalDataSaved: true,
			OTPVerified:       true,
		},
		CreatedAt: time.Date(2026, 9, 19, 7, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 19, 7, 30, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 9, 20, 7, 0, 0, 0, time.UTC),
		DeletedAt: &deleted,
	}

	data, err := json.Marshal(toCached(original))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var cached cachedSession
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cached.toDomain()

	if got.DeviceID != original.DeviceID {
		t.Errorf("device_id lost in cache round trip: got %q, want %q", got.DeviceID, original.DeviceID)
	}
	if got.ID != original.ID {
		t.Errorf("id lost: got %v, want %v", got.ID, original.ID)
	}
	if got.TNCVersion != original.TNCVersion {
		t.Errorf("tnc_version lost: got %q, want %q", got.TNCVersion, original.TNCVersion)
	}
	if !got.UpdatedAt.Equal(original.UpdatedAt) {
		t.Errorf("updated_at lost: got %v, want %v", got.UpdatedAt, original.UpdatedAt)
	}
	if !got.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("expires_at lost: got %v, want %v", got.ExpiresAt, original.ExpiresAt)
	}
	if got.DeletedAt == nil || !got.DeletedAt.Equal(deleted) {
		t.Errorf("deleted_at lost: got %v, want %v", got.DeletedAt, deleted)
	}
	if got.CurrentStep != original.CurrentStep || got.ProductType != original.ProductType {
		t.Errorf("step/product lost: %+v", got)
	}
	if got.StepsCompleted != original.StepsCompleted {
		t.Errorf("steps_completed lost: got %+v", got.StepsCompleted)
	}
}

func TestCachedSession_OmitsDeletedAtWhenLive(t *testing.T) {
	data, err := json.Marshal(toCached(&onboarding.Session{SessionID: "onb_live"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["deleted_at"]; present {
		t.Error("a live session should not carry deleted_at")
	}
}
