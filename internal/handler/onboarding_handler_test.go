package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The validation layer answers before any service is touched, so a handler
// built with nil services is exactly the right instrument for testing it: if a
// request ever got past validation, the test would panic instead of passing.
func newValidationOnlyHandler() *OnboardingHandler {
	return NewOnboardingHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func postJSON(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func errorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not the standard envelope: %v (%s)", err, rr.Body.String())
	}
	return envelope.Error.Code
}

// encryption_key_id is NOT NULL in onboarding_credentials. Without this check
// the request reached the insert and came back as a 500 — a client mistake
// reported as a server fault. VALIDATION_ERROR is a 400 in this API.
func TestSetCredentials_RequiresEncryptionKeyID(t *testing.T) {
	h := newValidationOnlyHandler()

	rr := postJSON(t, h.SetCredentials, `{
		"session_id": "onb_123",
		"access_code_encrypted": "abc",
		"pin_encrypted": "def"
	}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR, got %s", code)
	}
}

func TestSetCredentials_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no session":     `{"access_code_encrypted":"a","pin_encrypted":"b","encryption_key_id":"k"}`,
		"no access code": `{"session_id":"onb_1","pin_encrypted":"b","encryption_key_id":"k"}`,
		"no pin":         `{"session_id":"onb_1","access_code_encrypted":"a","encryption_key_id":"k"}`,
		"malformed json": `{"session_id":`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.SetCredentials, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestCreateSession_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no product":  `{"device_id":"dev-1","accepted_tnc_version":"2026-09-01"}`,
		"no device":   `{"product_type":"TAHAPAN_BCA","accepted_tnc_version":"2026-09-01"}`,
		"no tnc":      `{"product_type":"TAHAPAN_BCA","device_id":"dev-1"}`,
		"empty body":  `{}`,
		"not an obj":  `[]`,
		"broken json": `{"device_id"`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.CreateSession, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestVerifyAndResendOTP_RejectIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	if rr := postJSON(t, h.VerifyOTP, `{"session_id":"onb_1"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("verify-otp without a code: expected 400, got %d", rr.Code)
	}
	if rr := postJSON(t, h.VerifyOTP, `{"otp_code":"123456"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("verify-otp without a session: expected 400, got %d", rr.Code)
	}
	if rr := postJSON(t, h.ResendOTP, `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("resend-otp without a session: expected 400, got %d", rr.Code)
	}
}

// A code that cannot possibly be an OTP is a malformed request, not a wrong
// guess: it must be turned away before it can spend one of the five attempts.
func TestVerifyOTP_RejectsMalformedCodes(t *testing.T) {
	h := newValidationOnlyHandler()

	malformed := map[string]string{
		"too short":     `{"session_id":"onb_1","otp_code":"12345"}`,
		"too long":      `{"session_id":"onb_1","otp_code":"1234567"}`,
		"letters":       `{"session_id":"onb_1","otp_code":"12a456"}`,
		"spaces":        `{"session_id":"onb_1","otp_code":"123 56"}`,
		"unicode digit": `{"session_id":"onb_1","otp_code":"１２３４５６"}`,
	}

	for name, body := range malformed {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.VerifyOTP, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestSubmitVideoCallResult_RejectsIncompleteBodies(t *testing.T) {
	h := newValidationOnlyHandler()

	cases := map[string]string{
		"no queue":   `{"session_id":"onb_1","result":"APPROVED"}`,
		"no result":  `{"session_id":"onb_1","queue_id":"q_1"}`,
		"no session": `{"queue_id":"q_1","result":"APPROVED"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rr := postJSON(t, h.SubmitVideoCallResult, body); rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rr.Code)
			}
		})
	}
}

func TestIssueAgentSignalingToken_RequiresQueueID(t *testing.T) {
	h := newValidationOnlyHandler()

	if rr := postJSON(t, h.IssueAgentSignalingToken, `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// Capture-quality signals are optional. A value that is absent or unparseable
// must read as "not reported" — never as a passing score, which would hand a
// client a way to opt out of the quality gate by sending garbage.
func TestOptionalCaptureSignals(t *testing.T) {
	if got := optionalFloat(""); got != nil {
		t.Errorf("empty value should be nil, got %v", *got)
	}
	if got := optionalFloat("  "); got != nil {
		t.Errorf("blank value should be nil, got %v", *got)
	}
	if got := optionalFloat("not-a-number"); got != nil {
		t.Errorf("unparseable value should be nil, got %v", *got)
	}
	if got := optionalFloat(" 91.5 "); got == nil || *got != 91.5 {
		t.Errorf("expected 91.5, got %v", got)
	}

	if got := optionalInt(""); got != nil {
		t.Errorf("empty value should be nil, got %v", *got)
	}
	if got := optionalInt("4.5"); got != nil {
		t.Errorf("a float is not a corner count, got %v", *got)
	}
	if got := optionalInt("4"); got == nil || *got != 4 {
		t.Errorf("expected 4, got %v", got)
	}
}
