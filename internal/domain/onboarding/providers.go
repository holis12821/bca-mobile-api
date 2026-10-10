package onboarding

import (
	"context"
	"log/slog"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// Providers bundles the external integrations the onboarding flow depends on.
//
// Every one of these used to be a Mock* wired unconditionally in router.New,
// regardless of APP_ENV. A production deployment therefore ran with an OCR
// engine that returned the same hardcoded KTP for every photo, a Dukcapil stub
// that recognised four test NIKs, a biometric engine that always passed with a
// liveness score of 98.2, object storage that discarded the uploads, and a core
// banking client that invented account numbers — and answered 200 throughout.
//
// ProvidersFor makes that choice once, from the environment: development gets
// the mocks, anything else gets implementations that refuse with
// PROVIDER_NOT_CONFIGURED until a real integration is wired in. Refusing is
// the honest answer; fabricating a verified identity is not.
type Providers struct {
	OCR         OCREngine
	Dukcapil    DukcapilClient
	Storage     ObjectStorage
	CoreBanking CoreBankingClient

	// Liveness replaces the old Biometric engine.
	//
	// The engine it replaces (MockBiometricEngine) returned LivenessScore 98.2,
	// FaceMatchScore 96.7 and ISOCompliant true for any bytes at all, including
	// none — and ProvidersFor handed it to every development and SIT build. Every
	// liveness check in those environments passed unconditionally.
	//
	// There is no mock here. A provider either verifies or it refuses.
	Liveness LivenessProvider

	// Integrity verifies Play Integrity tokens server-side.
	Integrity IntegrityVerifier
}

// LivenessProviderMode selects the provider by configuration (decision Q1b), so a
// certified vendor can replace the internal engine without touching the client.
type LivenessProviderMode string

const (
	// LivenessModeInternal is the built-in engine: deterministic layer plus the
	// ML layer when a FaceAnalyzer is available.
	LivenessModeInternal LivenessProviderMode = "internal"

	// LivenessModeStub accepts anything that survives the deterministic layer.
	// Development only, and refused outright anywhere else.
	LivenessModeStub LivenessProviderMode = "stub"
)

// DukcapilMode selects whether NIK is checked against a population registry.
type DukcapilMode string

const (
	// DukcapilModeOff skips the registry check entirely. The KTP is still
	// validated — structure, internal consistency and e-KTP layout — but nothing
	// claims the identity was confirmed against Dukcapil.
	//
	// This is the default, and it is the honest default for a deployment that has
	// no registry access. The alternative that used to run was worse than
	// skipping: the mock refused every NIK outside a four-entry test table, so a
	// real e-KTP was rejected with OCR_DUKCAPIL_MISMATCH.
	DukcapilModeOff DukcapilMode = "off"

	// DukcapilModeMock accepts only the four hardcoded test identities. Useful
	// for exercising the match and mismatch branches, useless with a real KTP.
	DukcapilModeMock DukcapilMode = "mock"

	// DukcapilModeReal expects a configured registry client. Without one the
	// provider refuses rather than assumes a match.
	DukcapilModeReal DukcapilMode = "real"
)

// ProviderOptions carries everything ProvidersWith needs to pick implementations.
//
// A struct rather than a growing parameter list: the previous signature had five
// positional arguments of which three were interfaces, and adding storage and
// registry choices to that would have made call sites unreadable.
type ProviderOptions struct {
	// LivenessMode selects the liveness provider.
	LivenessMode LivenessProviderMode

	// LivenessConfig holds the thresholds and policy for that provider.
	LivenessConfig LivenessConfig

	// FaceAnalyzer is the ML port, nil in this repo — see
	// NewInternalLivenessProvider for why.
	FaceAnalyzer FaceAnalyzer

	// Integrity verifies Play Integrity tokens. Nil becomes the no-op verifier,
	// which reports a verdict that is NOT acceptable.
	Integrity IntegrityVerifier

	// Storage is the object store for KTP photos. Nil in development falls back
	// to MockObjectStorage, which keeps nothing — see its own comment for the two
	// bugs that caused.
	Storage ObjectStorage

	// Dukcapil is the registry client, used only when DukcapilMode is "real".
	Dukcapil DukcapilClient

	// DukcapilMode decides whether the registry is consulted at all.
	DukcapilMode DukcapilMode

	// OCR reads text from the photo server-side. Nil means there is no
	// server-side engine, and OCRService falls back to the text the client's
	// on-device recognizer produced. It never falls back to inventing one.
	OCR OCREngine
}

// ProvidersFor returns the provider set appropriate to the environment.
func ProvidersFor(devMode bool) Providers {
	return ProvidersWith(devMode, ProviderOptions{
		LivenessMode:   LivenessModeInternal,
		LivenessConfig: DefaultLivenessConfig(),
		DukcapilMode:   DukcapilModeOff,
	})
}

// ProvidersWith builds the provider set from explicit options.
func ProvidersWith(devMode bool, opts ProviderOptions) Providers {
	integrity := opts.Integrity
	if integrity == nil {
		integrity = NewNoopIntegrityVerifier()
	}

	liveness := resolveLivenessProvider(
		devMode, opts.LivenessMode, opts.LivenessConfig, opts.FaceAnalyzer,
	)
	dukcapil := resolveDukcapil(opts.DukcapilMode, opts.Dukcapil)

	// There is deliberately no mock OCR engine any more. The one that existed
	// ignored the image bytes entirely and returned the same hardcoded KTP text
	// with confidence 99.4 — so a photo with no text in it at all came back as a
	// fully extracted identity, and every development build "verified" it. A nil
	// engine means the server does not read the photo itself, which OCRService
	// handles by using the client's on-device OCR text and refusing when there is
	// none.
	if devMode {
		storage := opts.Storage
		if storage == nil {
			slog.Warn("onboarding storage is not configured; KTP photos will be " +
				"discarded and face match will have no reference")
			storage = NewMockObjectStorage()
		}
		return Providers{
			OCR:         opts.OCR,
			Dukcapil:    dukcapil,
			Storage:     storage,
			CoreBanking: NewMockCoreBankingClient(),
			Liveness:    liveness,
			Integrity:   integrity,
		}
	}

	slog.Warn("onboarding external providers are not configured; " +
		"OCR, Dukcapil, storage and core banking will refuse with PROVIDER_NOT_CONFIGURED")

	storage := opts.Storage
	if storage == nil {
		storage = unconfiguredStorage{}
	}

	return Providers{
		OCR:         opts.OCR,
		Dukcapil:    dukcapil,
		Storage:     storage,
		CoreBanking: unconfiguredCoreBanking{},
		Liveness:    liveness,
		Integrity:   integrity,
	}
}

// resolveDukcapil picks the registry client for the configured mode.
//
// "off" returns nil, and nil is what tells OCRService to skip the check and
// report dukcapil_checked=false. That is the one answer that does not lie:
// before this, a nil client was read as "mock mode: assume match" and the
// response claimed dukcapil_match=true for an identity nobody had verified.
func resolveDukcapil(mode DukcapilMode, client DukcapilClient) DukcapilClient {
	switch mode {
	case DukcapilModeMock:
		return NewMockDukcapilClient()
	case DukcapilModeReal:
		if client == nil {
			slog.Error("DUKCAPIL_MODE=real but no registry client is configured; " +
				"every KTP will be refused with OCR_DUKCAPIL_UNAVAILABLE")
			return unconfiguredDukcapil{}
		}
		return client
	default:
		return nil
	}
}

// resolveLivenessProvider is the one place the stub can be chosen, and the one
// place that refuses to choose it outside development.
func resolveLivenessProvider(
	devMode bool,
	mode LivenessProviderMode,
	cfg LivenessConfig,
	analyzer FaceAnalyzer,
) LivenessProvider {
	if mode == LivenessModeStub {
		if !devMode {
			// Two independent conditions must hold for the stub to run, and this
			// is the second. A single misread environment variable in production
			// would otherwise turn liveness into a formality.
			slog.Error("liveness: LIVENESS_PROVIDER=stub ignored because APP_ENV is not " +
				"development; refusing every attempt instead")
			return unconfiguredLivenessProvider{}
		}
		return NewStubLivenessProvider(cfg)
	}

	if analyzer == nil {
		slog.Warn("liveness: internal provider has no face analyzer; the ML layer is " +
			"unavailable and every attempt will FAIL (fail-closed). " +
			"Configure a certified provider before production use.")
	}
	return NewInternalLivenessProvider(analyzer, cfg)
}

type unconfiguredDukcapil struct{}

func (unconfiguredDukcapil) VerifyNIK(context.Context, string, string) (bool, error) {
	return false, apperr.OCRDukcapilUnavailable
}

type unconfiguredStorage struct{}

func (unconfiguredStorage) Upload(context.Context, string, string, []byte, string) (string, error) {
	return "", apperr.ProviderNotConfigured
}

func (unconfiguredStorage) Download(context.Context, string, string) ([]byte, error) {
	return nil, apperr.ProviderNotConfigured
}

func (unconfiguredStorage) Delete(context.Context, string, string) error {
	return apperr.ProviderNotConfigured
}

type unconfiguredCoreBanking struct{}

func (unconfiguredCoreBanking) CreateAccount(context.Context, string, ProductType, string, string) (*CoreBankingResult, error) {
	return nil, apperr.ProviderNotConfigured
}

func (unconfiguredCoreBanking) IssueCard(context.Context, CardIssuanceRequest) (*CardIssuanceResult, error) {
	return nil, apperr.ProviderNotConfigured
}
