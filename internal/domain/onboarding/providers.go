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
	Biometric   BiometricEngine
	CoreBanking CoreBankingClient
}

// ProvidersFor returns the provider set appropriate to the environment.
func ProvidersFor(devMode bool) Providers {
	if devMode {
		return Providers{
			OCR:         NewMockOCREngine(),
			Dukcapil:    NewMockDukcapilClient(),
			Storage:     NewMockObjectStorage(),
			Biometric:   NewMockBiometricEngine(),
			CoreBanking: NewMockCoreBankingClient(),
		}
	}

	slog.Warn("onboarding external providers are not configured; " +
		"OCR, Dukcapil, biometrics, storage and core banking will refuse with PROVIDER_NOT_CONFIGURED")

	return Providers{
		OCR:         unconfiguredOCR{},
		Dukcapil:    unconfiguredDukcapil{},
		Storage:     unconfiguredStorage{},
		Biometric:   unconfiguredBiometric{},
		CoreBanking: unconfiguredCoreBanking{},
	}
}

type unconfiguredOCR struct{}

func (unconfiguredOCR) ExtractText(context.Context, []byte) (string, float64, error) {
	return "", 0, apperr.ProviderNotConfigured
}

type unconfiguredDukcapil struct{}

func (unconfiguredDukcapil) VerifyNIK(context.Context, string, string) (bool, error) {
	return false, apperr.OCRDukcapilUnavailable
}

type unconfiguredStorage struct{}

func (unconfiguredStorage) Upload(context.Context, string, string, []byte, string) (string, error) {
	return "", apperr.ProviderNotConfigured
}

func (unconfiguredStorage) Delete(context.Context, string, string) error {
	return apperr.ProviderNotConfigured
}

type unconfiguredBiometric struct{}

func (unconfiguredBiometric) Analyze(context.Context, []byte, [][]byte, []byte) (*FaceAnalysisResult, error) {
	return nil, apperr.ProviderNotConfigured
}

type unconfiguredCoreBanking struct{}

func (unconfiguredCoreBanking) CreateAccount(context.Context, string, ProductType, string, string) (*CoreBankingResult, error) {
	return nil, apperr.ProviderNotConfigured
}

func (unconfiguredCoreBanking) IssueCard(context.Context, CardIssuanceRequest) (*CardIssuanceResult, error) {
	return nil, apperr.ProviderNotConfigured
}
