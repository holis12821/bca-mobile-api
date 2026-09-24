package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// --- in-memory mocks for OCR ---

type mockOCREngine struct {
	rawText    string
	confidence float64
	err        error
	calls      int
}

func (m *mockOCREngine) ExtractText(_ context.Context, _ []byte) (string, float64, error) {
	m.calls++
	return m.rawText, m.confidence, m.err
}

type mockDukcapil struct {
	match bool
	err   error
}

func (m *mockDukcapil) VerifyNIK(_ context.Context, _, _ string) (bool, error) {
	return m.match, m.err
}

// mockStorage records what was uploaded and what was later deleted, which is
// how the "no orphaned KTP photo" expectation is checked.
type mockStorage struct {
	objects   map[string][]byte
	uploads   int
	deletes   int
	uploadErr error
}

func newMockStorage() *mockStorage {
	return &mockStorage{objects: make(map[string][]byte)}
}

func (m *mockStorage) Upload(_ context.Context, bucket, key string, data []byte, _ string) (string, error) {
	if m.uploadErr != nil {
		return "", m.uploadErr
	}
	m.uploads++
	m.objects[key] = data
	return "s3://" + bucket + "/" + key, nil
}

func (m *mockStorage) Delete(_ context.Context, _, key string) error {
	m.deletes++
	delete(m.objects, key)
	return nil
}

type mockOCRRateLimiter struct {
	allow bool
	err   error
}

func (m *mockOCRRateLimiter) CheckOCRAttempt(_ context.Context, _ string) (bool, error) {
	return m.allow, m.err
}

// timeoutErr satisfies net.Error so the Dukcapil timeout branch can be driven.
type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }
func (timeoutErr) Temporary() bool {
	return true
}

var _ net.Error = timeoutErr{}

const validKTPText = `PROVINSI DKI JAKARTA
NIK : 3174082104950001
Nama : MUHAMMAD ARDAN PRAYOGI
Tempat/Tgl Lahir : Jakarta, 21-04-1995
Jenis Kelamin : LAKI-LAKI
Alamat : Jl. Sudirman Kav. 45 No. 12B
`

type ocrFixture struct {
	svc         *OCRService
	sessions    *mockSessionRepo
	cache       *mockSessionCache
	results     *mockOCRResultRepo
	engine      *mockOCREngine
	dukcapil    *mockDukcapil
	storage     *mockStorage
	rateLimiter *mockOCRRateLimiter
	audit       *mockAuditRepo
}

func setupOCRService() *ocrFixture {
	f := &ocrFixture{
		sessions:    newMockSessionRepo(),
		cache:       newMockSessionCache(),
		results:     newMockOCRResultRepo(),
		engine:      &mockOCREngine{rawText: validKTPText, confidence: 99.4},
		dukcapil:    &mockDukcapil{match: true},
		storage:     newMockStorage(),
		rateLimiter: &mockOCRRateLimiter{allow: true},
		audit:       &mockAuditRepo{},
	}

	f.svc = NewOCRService(OCRServiceConfig{
		Sessions:    f.sessions,
		Cache:       f.cache,
		OCRResults:  f.results,
		OCREngine:   f.engine,
		Dukcapil:    f.dukcapil,
		Storage:     f.storage,
		RateLimiter: f.rateLimiter,
		AES:         nil, // no PII key configured — must not panic
		Audit:       f.audit,
	})
	return f
}

func (f *ocrFixture) session(step Step) string {
	sessionID := "onb_ocr_test"
	s := &Session{
		SessionID:      sessionID,
		DeviceID:       "dev_ocr",
		ProductType:    ProductTahapanBCA,
		CurrentStep:    step,
		StepsCompleted: StepsCompleted{TNCAccepted: true},
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	f.sessions.sessions[sessionID] = s
	f.cache.data[sessionID] = s
	return sessionID
}

func goodCapture() DeviceCaptureMeta {
	sharp := 92.0
	glare := 4.0
	corners := 4
	return DeviceCaptureMeta{
		AutoCaptured:    true,
		Resolution:      "1920x1080",
		SharpnessScore:  &sharp,
		GlareScore:      &glare,
		CornersDetected: &corners,
	}
}

func TestProcessKTP_Success(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)

	resp, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("jpeg-bytes"), goodCapture(), "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Extracted.NIK != "3174082104950001" {
		t.Errorf("expected parsed NIK, got %q", resp.Extracted.NIK)
	}
	if resp.CurrentStep != StepPersonalData {
		t.Errorf("expected PERSONAL_DATA, got %s", resp.CurrentStep)
	}
	if !resp.DukcapilMatch {
		t.Error("dukcapil_match should be true")
	}
	if f.storage.uploads != 1 || f.storage.deletes != 0 {
		t.Errorf("expected one retained upload, got %d uploads / %d deletes", f.storage.uploads, f.storage.deletes)
	}
	if f.sessions.sessions[sessionID].CurrentStep != StepPersonalData {
		t.Error("session should advance to PERSONAL_DATA")
	}
}

// A missing PII key used to be a nil-pointer dereference on the photo
// encryption path, because that call skipped the nil check every other
// encryption site had.
func TestProcessKTP_NoAESKeyDoesNotPanic(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)

	if _, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("jpeg-bytes"), goodCapture(), "127.0.0.1", "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.storage.objects) != 1 {
		t.Errorf("expected the photo to be stored, got %d objects", len(f.storage.objects))
	}
}

func TestProcessKTP_WrongStep(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepBiometric)

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected an invalid-step error")
	}
	if f.storage.uploads != 0 {
		t.Error("nothing should be uploaded for a request rejected on step")
	}
}

func TestProcessKTP_RateLimited(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)
	f.rateLimiter.allow = false

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
	if !errors.Is(err, error(apperr.RateLimitExceeded)) && apperr.From(err).Code != apperr.RateLimitExceeded.Code {
		t.Fatalf("expected rate limit error, got %v", err)
	}
	if f.storage.uploads != 0 {
		t.Error("a rate-limited request must not upload")
	}
}

func TestProcessKTP_NotAKTP(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)
	f.engine.rawText = "STRUK PEMBELIAN\nTOTAL : 45000"

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
	if apperr.From(err).Code != apperr.OCRNotKTP.Code {
		t.Fatalf("expected OCR_NOT_KTP, got %v", err)
	}
	// The photo is uploaded only after the document is accepted, so a rejected
	// document leaves nothing behind.
	if f.storage.uploads != 0 || len(f.storage.objects) != 0 {
		t.Errorf("rejected document must not leave a stored photo: %d uploads", f.storage.uploads)
	}
}

func TestProcessKTP_DukcapilMismatch(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)
	f.dukcapil.match = false

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
	if apperr.From(err).Code != apperr.OCRDukcapilMismatch.Code {
		t.Fatalf("expected OCR_DUKCAPIL_MISMATCH, got %v", err)
	}
	if len(f.storage.objects) != 0 {
		t.Error("mismatch must not leave a stored photo")
	}
}

func TestProcessKTP_DukcapilTimeoutVsFailure(t *testing.T) {
	t.Run("timeout is reported as retryable", func(t *testing.T) {
		f := setupOCRService()
		sessionID := f.session(StepOCR)
		f.dukcapil.err = timeoutErr{}

		_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
		if apperr.From(err).Code != apperr.OCRDukcapilTimeout.Code {
			t.Fatalf("expected OCR_DUKCAPIL_TIMEOUT, got %v", err)
		}
	})

	t.Run("other faults are not disguised as timeouts", func(t *testing.T) {
		f := setupOCRService()
		sessionID := f.session(StepOCR)
		f.dukcapil.err = fmt.Errorf("dukcapil returned HTTP 500")

		_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
		if apperr.From(err).Code != apperr.OCRDukcapilUnavailable.Code {
			t.Fatalf("expected OCR_DUKCAPIL_UNAVAILABLE, got %v", err)
		}
	})
}

// The quality gate used to be unreachable: assessPhotoQuality always returned
// HIGH, so these three error codes could never be produced.
func TestProcessKTP_QualityGate(t *testing.T) {
	blurry := goodCapture()
	lowSharp := 30.0
	blurry.SharpnessScore = &lowSharp

	glary := goodCapture()
	highGlare := 80.0
	glary.GlareScore = &highGlare

	cropped := goodCapture()
	threeCorners := 3
	cropped.CornersDetected = &threeCorners

	smallFrame := goodCapture()
	smallFrame.Resolution = "320x240"

	cases := []struct {
		name string
		meta DeviceCaptureMeta
		want string
	}{
		{"blurry", blurry, apperr.OCRPhotoBlurry.Code},
		{"glare", glary, apperr.OCRGlareDetected.Code},
		{"corners", cropped, apperr.OCRCornersMissing.Code},
		{"low resolution", smallFrame, apperr.OCRPhotoBlurry.Code},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupOCRService()
			sessionID := f.session(StepOCR)

			_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), tc.meta, "127.0.0.1", "test")
			if got := apperr.From(err).Code; got != tc.want {
				t.Fatalf("expected %s, got %s (%v)", tc.want, got, err)
			}
			if len(f.storage.objects) != 0 {
				t.Error("a rejected capture must not leave a stored photo")
			}
		})
	}
}

// A client that reports nothing is not penalised — only a reported bad signal
// rejects the capture.
func TestProcessKTP_NoQualitySignalsPasses(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)

	if _, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), DeviceCaptureMeta{}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Low engine confidence is itself evidence of a bad capture.
func TestProcessKTP_LowEngineConfidenceIsBlurry(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)
	f.engine.confidence = 40

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), DeviceCaptureMeta{}, "127.0.0.1", "test")
	if apperr.From(err).Code != apperr.OCRPhotoBlurry.Code {
		t.Fatalf("expected OCR_PHOTO_BLURRY, got %v", err)
	}
}

// If the row cannot be written, the photo that was just uploaded is removed
// rather than left orphaned in the bucket.
func TestProcessKTP_StoreFailureDiscardsPhoto(t *testing.T) {
	f := setupOCRService()
	sessionID := f.session(StepOCR)
	f.svc.ocrResults = failingOCRRepo{}

	_, err := f.svc.ProcessKTP(context.Background(), sessionID, []byte("x"), goodCapture(), "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected a store error")
	}
	if f.storage.deletes != 1 || len(f.storage.objects) != 0 {
		t.Errorf("uploaded photo should be discarded: %d deletes, %d objects", f.storage.deletes, len(f.storage.objects))
	}
}

type failingOCRRepo struct{}

func (failingOCRRepo) Create(_ context.Context, _ *OCRResult) error {
	return errors.New("insert failed")
}

func (failingOCRRepo) FindBySessionID(_ context.Context, _ string) (*OCRResult, error) {
	return nil, nil
}

func TestParseResolution(t *testing.T) {
	cases := []struct {
		in         string
		w, h       int
		wantParsed bool
	}{
		{"1920x1080", 1920, 1080, true},
		{" 640 X 480 ", 640, 480, true},
		{"", 0, 0, false},
		{"high", 0, 0, false},
		{"1920", 0, 0, false},
		{"-10x20", 0, 0, false},
	}
	for _, tc := range cases {
		w, h, ok := parseResolution(tc.in)
		if ok != tc.wantParsed || w != tc.w || h != tc.h {
			t.Errorf("parseResolution(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.in, w, h, ok, tc.w, tc.h, tc.wantParsed)
		}
	}
}
