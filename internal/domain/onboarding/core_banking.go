package onboarding

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
)

// MockCoreBankingClient simulates the core banking system for development.
type MockCoreBankingClient struct{}

func NewMockCoreBankingClient() *MockCoreBankingClient {
	return &MockCoreBankingClient{}
}

func (m *MockCoreBankingClient) CreateAccount(_ context.Context, sessionID string, productType ProductType, holderName, _ string) (*CoreBankingResult, error) {
	accNumber := generateMockAccountNumber()

	branch, branchCode := assignBranch(productType)

	slog.Info("[MOCK CORE BANKING] account created",
		"session_id", sessionID,
		"account_number", accNumber,
		"holder", holderName,
		"product", string(productType),
		"branch", branch,
	)

	return &CoreBankingResult{
		AccountNumber: accNumber,
		Branch:        branch,
		BranchCode:    branchCode,
	}, nil
}

// IssueCard simulates a card print request.
//
// Mock ini hanya hidup di development (ProvidersFor). Di lingkungan lain
// unconfiguredCoreBanking menolak dengan PROVIDER_NOT_CONFIGURED — gerbang yang
// sama dengan pembuatan rekening, karena memalsukan permintaan cetak kartu
// sama berbahayanya dengan memalsukan nomor rekening.
func (m *MockCoreBankingClient) IssueCard(_ context.Context, req CardIssuanceRequest) (*CardIssuanceResult, error) {
	masked := maskCardNumber(generateMockCardNumber())

	slog.Info("[MOCK CORE BANKING] card issuance requested",
		"session_id", req.SessionID,
		"card_type", req.CardType,
		"core_banking_code", req.CoreBankingCode,
		"masked_number", masked,
	)

	// Status awal selalu REQUESTED: pencetakan fisik berjalan di luar
	// permintaan ini, dan melaporkan PRINTING seketika akan menjanjikan
	// kemajuan yang belum terjadi.
	return &CardIssuanceResult{
		MaskedNumber: masked,
		Status:       CardIssuanceRequested,
	}, nil
}

// generateMockCardNumber returns 16 digits. Never logged in full, never stored.
func generateMockCardNumber() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	return fmt.Sprintf("5221%012d", n.Int64())
}

// maskCardNumber keeps the last four digits and nothing else.
//
// PAN lengkap tidak pernah disimpan maupun dicatat di layanan ini: yang
// ditampilkan ke nasabah cukup empat digit terakhir (docs/04-SECURITY.md).
func maskCardNumber(pan string) string {
	if len(pan) < 4 {
		return "••••"
	}
	return "•••• " + pan[len(pan)-4:]
}

// generateMockAccountNumber generates a 10-digit mock account number.
func generateMockAccountNumber() string {
	// Prefix "542" (BCA range) + 7 random digits
	n, _ := rand.Int(rand.Reader, big.NewInt(10_000_000))
	return fmt.Sprintf("542%07d", n.Int64())
}

// assignBranch returns a mock branch based on product type.
func assignBranch(_ ProductType) (string, string) {
	return "KCU Jakarta Thamrin", "0539"
}
