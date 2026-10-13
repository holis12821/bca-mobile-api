package onboarding

import (
	"bytes"
	"context"
	gocrypto "crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math/bits"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// internalLivenessProvider is the built-in engine: a deterministic layer that
// needs no model, plus an ML layer behind the FaceAnalyzer port.
//
// The deterministic layer is the half that actually runs today. The ML layer —
// one face per frame, server-side pose re-detection, same-person continuity,
// passive PAD and face match — is BLOCKED: it needs face-detection, embedding and
// PAD models, and no weights for those are available under a licence that permits
// commercial use (§1b "Model licensing rule" of the Phase 2 decisions). When a
// vendor or a licensed model arrives, FaceAnalyzer is implemented and nothing
// else here changes.
//
// With analyzer == nil the provider refuses. That is the fail-closed rule, and it
// means a production deployment answers FAIL for every attempt until a provider
// is configured. That is the documented, intended behaviour — not a pass.
type internalLivenessProvider struct {
	analyzer FaceAnalyzer
	cfg      LivenessConfig
}

// NewInternalLivenessProvider builds the built-in provider. analyzer may be nil.
func NewInternalLivenessProvider(analyzer FaceAnalyzer, cfg LivenessConfig) LivenessProvider {
	return &internalLivenessProvider{analyzer: analyzer, cfg: cfg}
}

func (p *internalLivenessProvider) Name() string { return "internal" }

func (p *internalLivenessProvider) Verify(
	ctx context.Context,
	challenge *LivenessChallenge,
	submission *LivenessSubmission,
	reference []byte,
) (*LivenessVerdict, error) {
	if challenge == nil || submission == nil {
		return refuse("missing challenge or submission"), nil
	}

	// --- Layer 1: deterministic. No model needed, so this always runs. ---
	if reason := p.checkDeterministic(challenge, submission); reason != "" {
		return refuse(reason), nil
	}

	// --- Layer 2: ML. Unavailable means FAIL, never PASS. ---
	if p.analyzer == nil {
		return &LivenessVerdict{
			Passed:      false,
			Unavailable: true,
			Reason:      "ml_layer_unavailable",
		}, nil
	}
	return p.checkWithModels(ctx, challenge, submission, reference)
}

// checkDeterministic returns an empty string when every model-free check passes.
func (p *internalLivenessProvider) checkDeterministic(
	challenge *LivenessChallenge,
	submission *LivenessSubmission,
) string {
	// The nonce is consumed by the caller before this runs, but the pairing is
	// re-asserted here: a provider that assumes its inputs match is a provider
	// that passes a submission answering a different challenge.
	if submission.ChallengeID != challenge.ChallengeID || submission.Nonce != challenge.Nonce {
		return "challenge_mismatch"
	}
	if submission.DeviceID != challenge.DeviceID {
		return "device_mismatch"
	}
	if challenge.DeviceKeyID != "" && submission.DeviceKeyID != challenge.DeviceKeyID {
		return "device_key_mismatch"
	}

	if len(submission.NeutralFrame) == 0 {
		return "missing_neutral_frame"
	}
	if len(submission.StepFrames) != len(challenge.Actions) {
		return "frame_count_mismatch"
	}

	// Every action of the challenge must appear exactly once, at the index the
	// server assigned it. Anything else is answering a different challenge.
	for i, expected := range challenge.Actions {
		step := submission.StepFrames[i]
		if step.Index != i {
			return "step_index_mismatch"
		}
		if step.Action != expected {
			return "step_action_mismatch"
		}
		if len(step.JPEG) == 0 {
			return "empty_step_frame"
		}
	}

	// Timestamps must advance and sit inside the challenge window. A submission
	// assembled offline from old frames fails here.
	lower := challenge.IssuedAt.Add(-p.cfg.MaxClockSkew)
	upper := challenge.ExpiresAt.Add(p.cfg.MaxClockSkew)
	var previous time.Time
	for _, step := range submission.StepFrames {
		if step.CapturedAt.Before(lower) || step.CapturedAt.After(upper) {
			return "timestamp_outside_window"
		}
		if !previous.IsZero() && step.CapturedAt.Before(previous) {
			return "timestamp_not_monotonic"
		}
		previous = step.CapturedAt
	}

	return p.checkFrameDistinctness(submission)
}

// checkFrameDistinctness rejects a still image submitted for every step.
//
// Two rules, not one, and the split matters. No two frames may be perceptually
// identical — that is the still-image case. On top of that, each head-pose frame
// must differ from the neutral frame by a real margin, because a head turn
// changes the picture substantially.
//
// The blink frame is deliberately exempt from the second rule: the client captures
// it with the eyes closed, which is a small change to the picture. Requiring a
// large distance there would reject honest customers, and the eyes being closed is
// checked properly in the ML layer instead.
func (p *internalLivenessProvider) checkFrameDistinctness(submission *LivenessSubmission) string {
	type hashed struct {
		action LivenessAction
		hash   uint64
		ok     bool
	}

	neutralHash, neutralOK := averageHash(submission.NeutralFrame)
	if !neutralOK {
		return "neutral_frame_undecodable"
	}

	frames := make([]hashed, 0, len(submission.StepFrames))
	for _, step := range submission.StepFrames {
		h, ok := averageHash(step.JPEG)
		if !ok {
			return "step_frame_undecodable"
		}
		frames = append(frames, hashed{action: step.Action, hash: h, ok: ok})
	}

	for _, f := range frames {
		if hammingDistance(f.hash, neutralHash) == 0 {
			return "duplicate_frame"
		}
		if f.action.IsHeadPose() &&
			hammingDistance(f.hash, neutralHash) < p.cfg.MinFrameDistance {
			return "frame_too_similar_to_neutral"
		}
	}
	for i := 0; i < len(frames); i++ {
		for j := i + 1; j < len(frames); j++ {
			if hammingDistance(frames[i].hash, frames[j].hash) == 0 {
				return "duplicate_frame"
			}
		}
	}
	return ""
}

// checkWithModels is the ML layer. Reached only when a FaceAnalyzer exists.
func (p *internalLivenessProvider) checkWithModels(
	ctx context.Context,
	challenge *LivenessChallenge,
	submission *LivenessSubmission,
	reference []byte,
) (*LivenessVerdict, error) {
	neutral, err := p.analyzer.Analyze(ctx, submission.NeutralFrame)
	if err != nil {
		return nil, fmt.Errorf("analyze neutral frame: %w", err)
	}
	if neutral.FaceCount != 1 {
		return refuse("neutral_frame_face_count"), nil
	}

	for i, step := range submission.StepFrames {
		analysis, err := p.analyzer.Analyze(ctx, step.JPEG)
		if err != nil {
			return nil, fmt.Errorf("analyze step %d: %w", i, err)
		}
		if analysis.FaceCount != 1 {
			return refuse("step_frame_face_count"), nil
		}

		// The server re-detects the pose rather than believing the client's claim
		// about which movement the frame shows. This is the check that makes the
		// randomised action order mean anything.
		if !poseMatches(step.Action, analysis) {
			return refuse("step_pose_mismatch"), nil
		}

		// Same person across every frame; otherwise each step could be performed
		// by a different face.
		if p.analyzer.SamePerson(neutral.Embedding, analysis.Embedding) < samePersonThreshold {
			return refuse("person_changed_mid_challenge"), nil
		}
	}

	padScore, err := p.analyzer.SpoofScore(ctx, submission.NeutralFrame)
	if err != nil {
		return nil, fmt.Errorf("pad score: %w", err)
	}
	livenessScore := padScore * 100
	if livenessScore < p.cfg.LivenessThreshold {
		return &LivenessVerdict{
			Passed:        false,
			LivenessScore: livenessScore,
			Reason:        "pad_below_threshold",
		}, nil
	}

	// No reference means no face match is possible. Passing anyway would mean
	// confirming a live face without confirming whose.
	if len(reference) == 0 {
		return &LivenessVerdict{
			Passed:        false,
			LivenessScore: livenessScore,
			Unavailable:   true,
			Reason:        "no_enrolled_reference",
		}, nil
	}

	referenceAnalysis, err := p.analyzer.Analyze(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("analyze reference: %w", err)
	}
	matchScore := p.analyzer.SamePerson(neutral.Embedding, referenceAnalysis.Embedding) * 100
	matched := matchScore >= p.cfg.FaceMatchThreshold

	return &LivenessVerdict{
		Passed:          matched,
		LivenessScore:   livenessScore,
		FaceMatchPassed: matched,
		FaceMatchScore:  matchScore,
		Reason: func() string {
			if matched {
				return "passed"
			}
			return "face_match_below_threshold"
		}(),
	}, nil
}

// poseMatches checks a re-detected frame against the action it is supposed to show.
//
// The thresholds are looser than the client's: the client must be confident enough
// to advance, the server only has to confirm the movement happened, and being
// stricter here would reject frames the client correctly accepted.
func poseMatches(action LivenessAction, a *FrameFaceAnalysis) bool {
	switch action {
	case ActionTurnLeft:
		return a.YawDegrees >= serverTurnDegrees
	case ActionTurnRight:
		return a.YawDegrees <= -serverTurnDegrees
	case ActionLookUp:
		return a.PitchDegrees >= serverLookUpDegrees
	case ActionLookDown:
		return a.PitchDegrees <= -serverLookDownDegrees
	case ActionBlink:
		// The blink evidence frame is captured with the eyes closed, so this is
		// the frame that proves the blink happened.
		return a.EyeOpenProbability <= serverEyeClosed
	}
	return false
}

func refuse(reason string) *LivenessVerdict {
	return &LivenessVerdict{Passed: false, Reason: reason}
}

// --- Perceptual hash ---

// averageHash reduces a JPEG to a 64-bit fingerprint (8x8 grayscale vs its mean).
//
// aHash rather than pHash/DCT: the only question asked of it is "is this the same
// picture again", for which aHash is sufficient and has no dependency. It is not
// used for anything that needs real perceptual similarity.
func averageHash(data []byte) (uint64, bool) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, false
	}

	var samples [hashSide * hashSide]float64
	var sum float64
	bounds := img.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return 0, false
	}

	for y := 0; y < hashSide; y++ {
		for x := 0; x < hashSide; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/hashSide
			sy := bounds.Min.Y + y*bounds.Dy()/hashSide
			value := grayValue(img, sx, sy)
			samples[y*hashSide+x] = value
			sum += value
		}
	}

	mean := sum / float64(len(samples))
	var hash uint64
	for i, value := range samples {
		if value >= mean {
			hash |= 1 << uint(i)
		}
	}
	return hash, true
}

func grayValue(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	// Rec. 601 luma; the absolute scale does not matter because only the
	// comparison against the frame's own mean is used.
	return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
}

func hammingDistance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// --- Stub provider (development only) ---

// stubLivenessProvider passes any submission that survives the deterministic layer.
//
// It exists so the flow can be driven end to end locally, and it is impossible to
// reach in production: ProvidersFor only builds it when APP_ENV is development AND
// the stub is explicitly switched on. §1b of the Phase 2 decisions requires both.
type stubLivenessProvider struct {
	deterministic LivenessProvider
}

func NewStubLivenessProvider(cfg LivenessConfig) LivenessProvider {
	slog.Warn("liveness: STUB provider active — every submission that passes the " +
		"deterministic checks is accepted. Development only.")
	return &stubLivenessProvider{deterministic: NewInternalLivenessProvider(nil, cfg)}
}

func (p *stubLivenessProvider) Name() string { return "stub" }

func (p *stubLivenessProvider) Verify(
	ctx context.Context,
	challenge *LivenessChallenge,
	submission *LivenessSubmission,
	reference []byte,
) (*LivenessVerdict, error) {
	// The deterministic layer still applies: nonce pairing, frame count, step
	// order, timestamps and frame distinctness are all model-free, so there is no
	// reason for the stub to skip them. A replayed still image fails even here.
	verdict, err := p.deterministic.Verify(ctx, challenge, submission, reference)
	if err != nil {
		return nil, err
	}
	if !verdict.Unavailable {
		// A real deterministic refusal stands.
		return verdict, nil
	}
	return &LivenessVerdict{
		Passed:          true,
		LivenessScore:   100,
		FaceMatchPassed: true,
		FaceMatchScore:  100,
		Reason:          "stub_provider",
	}, nil
}

// --- Unconfigured provider ---

// unconfiguredLivenessProvider is what non-development environments get until a
// real provider is wired in. It refuses; it does not pass.
type unconfiguredLivenessProvider struct{}

func (unconfiguredLivenessProvider) Name() string { return "unconfigured" }

func (unconfiguredLivenessProvider) Verify(
	context.Context, *LivenessChallenge, *LivenessSubmission, []byte,
) (*LivenessVerdict, error) {
	return &LivenessVerdict{
		Passed:      false,
		Unavailable: true,
		Reason:      "provider_not_configured",
	}, nil
}

// --- Play Integrity ---

// noopIntegrityVerifier is used when no credentials are configured.
//
// It reports a verdict that is NOT acceptable. Whether that refuses the attempt is
// the caller's policy decision via LivenessConfig.IntegrityLogOnly, which is only
// ever true outside production.
type noopIntegrityVerifier struct{}

func (noopIntegrityVerifier) Verify(context.Context, string, string) (*IntegrityVerdict, error) {
	return &IntegrityVerdict{Verdicts: []string{"NOT_CONFIGURED"}}, nil
}

// NewNoopIntegrityVerifier returns the verifier used when Play Integrity is unset.
func NewNoopIntegrityVerifier() IntegrityVerifier { return noopIntegrityVerifier{} }

const (
	// Server-side pose thresholds, looser than the client's by design: the client
	// must be confident enough to advance the step, the server only has to confirm
	// the movement is present in the frame. Being stricter here would reject frames
	// the client was right to accept.
	serverTurnDegrees     = 18.0
	serverLookUpDegrees   = 14.0
	serverLookDownDegrees = 10.0

	// The blink evidence frame is captured with the eyes closed.
	serverEyeClosed = 0.35

	// Cosine similarity below this means the face changed between frames.
	samePersonThreshold = 0.75

	// 8x8 grayscale: 64 bits, which is what averageHash returns.
	hashSide = 8
)

// PlayIntegrityConfig is what the Play Integrity verifier needs to work.
//
// Every field is a secret or an identity that must come from configuration. None
// of it is ever committed — see the config-key list in the Phase 2 report.
type PlayIntegrityConfig struct {
	// PackageName must equal the Android applicationId: id.bca.bcamobile.
	PackageName string

	// CertificateSHA256 is the base64url SHA-256 of the signing certificate, as
	// Play reports it. Empty means the digest is not checked, which is only
	// acceptable outside production.
	CertificateSHA256 string

	ClientEmail string
	PrivateKey  *rsa.PrivateKey
	TokenURI    string
	Timeout     time.Duration
}

// playIntegrityVerifier decodes a token through Google's Play Integrity API.
//
// The token is never inspected on the device: it is signed by Google and means
// nothing until Google decodes it (decision Q3). Hand-rolled over net/http rather
// than pulling google.golang.org/api, following the same reasoning as
// internal/pkg/push/fcm.go — one signed assertion and one POST, and this module
// stays thin on dependencies.
type playIntegrityVerifier struct {
	cfg  PlayIntegrityConfig
	http *http.Client

	mu       sync.Mutex
	bearer   string
	bearerTo time.Time
	nowFunc  func() time.Time
}

// NewPlayIntegrityVerifier returns a verifier, or the no-op one when unconfigured.
func NewPlayIntegrityVerifier(cfg PlayIntegrityConfig) IntegrityVerifier {
	if cfg.PackageName == "" || cfg.ClientEmail == "" || cfg.PrivateKey == nil {
		slog.Warn("play integrity is not configured; liveness attempts carry no verified " +
			"device verdict. In production this refuses every attempt.")
		return NewNoopIntegrityVerifier()
	}
	if cfg.TokenURI == "" {
		cfg.TokenURI = "https://oauth2.googleapis.com/token"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &playIntegrityVerifier{
		cfg:     cfg,
		http:    &http.Client{Timeout: cfg.Timeout},
		nowFunc: time.Now,
	}
}

func (v *playIntegrityVerifier) Verify(
	ctx context.Context,
	token, expectedNonce string,
) (*IntegrityVerdict, error) {
	if token == "" {
		// A missing token is not an error to investigate; it is simply a verdict
		// that cannot be accepted. The caller's policy decides what that means.
		return &IntegrityVerdict{Verdicts: []string{"TOKEN_ABSENT"}}, nil
	}

	bearer, err := v.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]string{"integrity_token": token})
	if err != nil {
		return nil, fmt.Errorf("encode integrity request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"https://playintegrity.googleapis.com/v1/%s:decodeIntegrityToken",
		url.PathEscape(v.cfg.PackageName),
	)

	ctx, cancel := context.WithTimeout(ctx, v.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build integrity request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("decode integrity token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A 400 here usually means the token is malformed or replayed, which is a
		// verdict rather than an outage; it still must not pass.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return &IntegrityVerdict{
				Verdicts: []string{fmt.Sprintf("DECODE_REJECTED_%d", resp.StatusCode)},
			}, nil
		}
		return nil, fmt.Errorf("decode integrity token: http %d", resp.StatusCode)
	}

	var decoded struct {
		TokenPayloadExternal struct {
			RequestDetails struct {
				RequestPackageName string `json:"requestPackageName"`
				Nonce              string `json:"nonce"`
				RequestHash        string `json:"requestHash"`
				TimestampMillis    string `json:"timestampMillis"`
			} `json:"requestDetails"`
			AppIntegrity struct {
				AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
				PackageName             string   `json:"packageName"`
				CertificateSha256Digest []string `json:"certificateSha256Digest"`
			} `json:"appIntegrity"`
			DeviceIntegrity struct {
				DeviceRecognitionVerdict []string `json:"deviceRecognitionVerdict"`
			} `json:"deviceIntegrity"`
		} `json:"tokenPayloadExternal"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode integrity response: %w", err)
	}

	payload := decoded.TokenPayloadExternal
	verdict := &IntegrityVerdict{
		Verdicts: append(
			[]string{payload.AppIntegrity.AppRecognitionVerdict},
			payload.DeviceIntegrity.DeviceRecognitionVerdict...,
		),
	}

	for _, name := range payload.DeviceIntegrity.DeviceRecognitionVerdict {
		if name == "MEETS_DEVICE_INTEGRITY" || name == "MEETS_STRONG_INTEGRITY" {
			verdict.MeetsDeviceIntegrity = true
			break
		}
	}

	// Package name AND signing certificate: the package name alone is just a
	// string a repackaged APK can also claim.
	packageOK := payload.RequestDetails.RequestPackageName == v.cfg.PackageName ||
		payload.AppIntegrity.PackageName == v.cfg.PackageName
	certOK := v.cfg.CertificateSHA256 == "" ||
		slices.Contains(payload.AppIntegrity.CertificateSha256Digest, v.cfg.CertificateSHA256)
	recognised := payload.AppIntegrity.AppRecognitionVerdict == "PLAY_RECOGNIZED"
	verdict.PackageMatches = packageOK && certOK && recognised

	// The nonce ties the token to this challenge. Without it a token minted for an
	// earlier attempt satisfies a later one.
	verdict.NonceMatches = expectedNonce != "" &&
		(payload.RequestDetails.Nonce == expectedNonce ||
			payload.RequestDetails.RequestHash == expectedNonce)

	return verdict, nil
}

// accessToken mints and caches an OAuth2 bearer for the Play Integrity scope.
func (v *playIntegrityVerifier) accessToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.bearer != "" && v.nowFunc().Add(integrityTokenSkew).Before(v.bearerTo) {
		return v.bearer, nil
	}

	assertion, err := v.signedAssertion()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}

	ctx, cancel := context.WithTimeout(ctx, v.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, v.cfg.TokenURI, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", fmt.Errorf("build integrity token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint integrity access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint integrity access token: http %d", resp.StatusCode)
	}

	var minted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		return "", fmt.Errorf("decode integrity token response: %w", err)
	}
	if minted.AccessToken == "" {
		return "", errors.New("mint integrity access token: response carried no access_token")
	}
	if minted.ExpiresIn <= 0 {
		minted.ExpiresIn = 3600
	}

	v.bearer = minted.AccessToken
	v.bearerTo = v.nowFunc().Add(time.Duration(minted.ExpiresIn) * time.Second)
	return v.bearer, nil
}

// signedAssertion builds the RS256 JWT that buys an access token.
func (v *playIntegrityVerifier) signedAssertion() (string, error) {
	now := v.nowFunc()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   v.cfg.ClientEmail,
		"scope": playIntegrityScope,
		"aud":   v.cfg.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(nil, v.cfg.PrivateKey, cryptoSHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign integrity assertion: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

const (
	playIntegrityScope = "https://www.googleapis.com/auth/playintegrity"
	integrityTokenSkew = 60 * time.Second
	cryptoSHA256       = gocrypto.SHA256
)
