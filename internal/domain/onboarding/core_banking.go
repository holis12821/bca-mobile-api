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