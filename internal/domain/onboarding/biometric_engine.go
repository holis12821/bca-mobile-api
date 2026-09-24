package onboarding

import (
	"context"
	"log/slog"
)

// MockBiometricEngine returns successful biometric results for development.
type MockBiometricEngine struct{}

func NewMockBiometricEngine() *MockBiometricEngine {
	return &MockBiometricEngine{}
}

func (m *MockBiometricEngine) Analyze(_ context.Context, _ []byte, livenessFrames [][]byte, _ []byte) (*FaceAnalysisResult, error) {
	slog.Info("mock biometric engine: analyze",
		"frame_count", len(livenessFrames),
	)

	return &FaceAnalysisResult{
		FaceCount:      1,
		LivenessScore:  98.2,
		FaceMatchScore: 96.7,
		ISOCompliant:   true,
		SpoofDetected:  false,
		Quality:        "HIGH",
	}, nil
}
