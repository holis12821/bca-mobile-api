package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://bcamobile:localdev_password_123@localhost:5432/bcamobile?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	log.Println("seeding database...")

	seed(ctx, pool)

	log.Println("seed complete!")
}

type seedUser struct {
	ID          uuid.UUID
	FullName    string
	PIN         string
	Phone       string
	Email       string
	DeviceID    string
	DeviceModel string
	Accounts    []seedAccount
}

type seedAccount struct {
	ID            uuid.UUID
	AccountNumber string
	AccountLabel  string
	Balance       decimal.Decimal
}

func seed(ctx context.Context, pool *pgxpool.Pool) {
	now := time.Now().UTC()

	// Hash PINs with Argon2id
	hashPIN := func(pin string) string {
		h, err := crypto.HashPassword(ctx, pin, crypto.DefaultArgon2Params)
		if err != nil {
			log.Fatalf("hash pin: %v", err)
		}
		return h
	}

	users := []seedUser{
		{
			ID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			FullName:    "NURHOLIS MAJID",
			PIN:         "123456",
			Phone:       "081234567890",
			Email:       "nurholis@example.com",
			DeviceID:    "device-nurholis-001",
			DeviceModel: "iPhone 15 Pro Max",
			Accounts: []seedAccount{
				{
					ID:            uuid.MustParse("00000000-0000-0000-0000-100000000001"),
					AccountNumber: "1234567890",
					AccountLabel:  "Tabungan Utama",
					Balance:       decimal.NewFromInt(50000000),
				},
				{
					ID:            uuid.MustParse("00000000-0000-0000-0000-100000000002"),
					AccountNumber: "1234567891",
					AccountLabel:  "Tabungan Cadangan",
					Balance:       decimal.NewFromInt(10000000),
				},
			},
		},
		{
			ID:          uuid.MustParse("00000000-0000-0000-0000-000000000002"),
			FullName:    "BUDI SANTOSO",
			PIN:         "654321",
			Phone:       "081298765432",
			Email:       "budi@example.com",
			DeviceID:    "device-budi-001",
			DeviceModel: "Samsung Galaxy S24 Ultra",
			Accounts: []seedAccount{
				{
					ID:            uuid.MustParse("00000000-0000-0000-0000-200000000001"),
					AccountNumber: "9876543210",
					AccountLabel:  "Tabungan",
					Balance:       decimal.NewFromInt(25000000),
				},
			},
		},
		{
			ID:          uuid.MustParse("00000000-0000-0000-0000-000000000003"),
			FullName:    "SITI RAHAYU",
			PIN:         "111111",
			Phone:       "081355512345",
			Email:       "siti@example.com",
			DeviceID:    "device-siti-001",
			DeviceModel: "iPhone 14",
			Accounts: []seedAccount{
				{
					ID:            uuid.MustParse("00000000-0000-0000-0000-300000000001"),
					AccountNumber: "5555666677",
					AccountLabel:  "Tabungan Bisnis",
					Balance:       decimal.NewFromInt(100000000),
				},
			},
		},
	}

	for _, u := range users {
		pinHash := hashPIN(u.PIN)

		// Insert user (upsert)
		_, err := pool.Exec(ctx, `
			INSERT INTO users (id, full_name, pin_hash, phone_number, email, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $6)
			ON CONFLICT (id) DO UPDATE SET
				full_name = EXCLUDED.full_name,
				pin_hash = EXCLUDED.pin_hash,
				phone_number = EXCLUDED.phone_number,
				email = EXCLUDED.email,
				updated_at = EXCLUDED.updated_at`,
			u.ID, u.FullName, pinHash, u.Phone, u.Email, now)
		if err != nil {
			log.Fatalf("insert user %s: %v", u.FullName, err)
		}
		log.Printf("  user: %s (PIN: %s)", u.FullName, u.PIN)

		// Insert device
		_, err = pool.Exec(ctx, `
			INSERT INTO devices (id, user_id, device_id, device_model, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT DO NOTHING`,
			uuid.New(), u.ID, u.DeviceID, u.DeviceModel, now)
		if err != nil {
			log.Fatalf("insert device for %s: %v", u.FullName, err)
		}

		// Insert accounts
		for _, a := range u.Accounts {
			_, err = pool.Exec(ctx, `
				INSERT INTO accounts (id, user_id, account_number, account_type, account_label,
					balance, currency, status, created_at, updated_at)
				VALUES ($1, $2, $3, 'SAVINGS', $4, $5, 'IDR', 'ACTIVE', $6, $6)
				ON CONFLICT (id) DO UPDATE SET
					balance = EXCLUDED.balance,
					account_label = EXCLUDED.account_label,
					updated_at = EXCLUDED.updated_at`,
				a.ID, u.ID, a.AccountNumber, a.AccountLabel, a.Balance, now)
			if err != nil {
				log.Fatalf("insert account %s: %v", a.AccountNumber, err)
			}
			log.Printf("    account: %s (%s) balance: %s", a.AccountNumber, a.AccountLabel, a.Balance.String())
		}

		// Insert default transaction limits
		limits := []struct {
			Type    string
			Daily   int64
			Ceiling int64
		}{
			{"TRANSFER_INTERNAL", 50000000, 100000000},
			{"TRANSFER_EXTERNAL", 25000000, 100000000},
			{"EWALLET", 20000000, 20000000},
			{"QRIS", 5000000, 5000000},
		}
		for _, l := range limits {
			_, err = pool.Exec(ctx, `
				INSERT INTO transaction_limits (id, user_id, limit_type, daily_limit, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $5)
				ON CONFLICT (user_id, limit_type) DO UPDATE SET
					daily_limit = EXCLUDED.daily_limit,
					updated_at = EXCLUDED.updated_at`,
				uuid.New(), u.ID, l.Type, l.Daily, now)
			if err != nil {
				log.Fatalf("insert limit %s for %s: %v", l.Type, u.FullName, err)
			}
		}
	}

	// Seed initial mutations for user 1 (recent activity)
	seedMutations(ctx, pool, users[0], now)

	// Seed notifications
	seedNotifications(ctx, pool, users[0].ID, now)

	// Seed ewallet providers
	seedEWalletProviders(ctx, pool)

	// Seed promotions
	seedPromotions(ctx, pool, now)
}

func seedMutations(ctx context.Context, pool *pgxpool.Pool, user seedUser, now time.Time) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	wibNow := now.In(loc)
	account := user.Accounts[0]

	mutations := []struct {
		Type        string
		Amount      decimal.Decimal
		Desc        string
		Detail      string
		Category    string
		DaysAgo     int
	}{
		{"CREDIT", decimal.NewFromInt(5000000), "TRANSFER MASUK", "Transfer dari BUDI SANTOSO", "TRANSFER", 0},
		{"DEBIT", decimal.NewFromInt(150000), "QRIS TOKO SEJAHTERA", "Pembayaran QRIS", "QRIS", 1},
		{"DEBIT", decimal.NewFromInt(500000), "TOP UP GOPAY", "Top Up 081234567890", "EWALLET", 1},
		{"CREDIT", decimal.NewFromInt(15000000), "TRANSFER MASUK", "Gaji PT BERKAH MAKMUR", "TRANSFER", 3},
		{"DEBIT", decimal.NewFromInt(2500000), "TRANSFER KELUAR", "Transfer ke 9876543210 BUDI SANTOSO", "TRANSFER", 5},
		{"DEBIT", decimal.NewFromInt(75000), "QRIS WARUNG PADANG", "Pembayaran QRIS", "QRIS", 7},
	}

	balance := account.Balance
	for _, m := range mutations {
		txnDate := wibNow.AddDate(0, 0, -m.DaysAgo)
		dateStr := txnDate.Format("2006-01-02")
		timeStr := txnDate.Format("15:04:05")

		var balBefore, balAfter decimal.Decimal
		if m.Type == "DEBIT" {
			balBefore = balance
			balAfter = balance.Sub(m.Amount)
			balance = balAfter
		} else {
			balAfter = balance
			balBefore = balance.Sub(m.Amount)
		}

		refNum := fmt.Sprintf("REF%s%04d", txnDate.Format("20060102"), m.DaysAgo)

		_, err := pool.Exec(ctx, `
			INSERT INTO account_mutations (id, account_id, mutation_type,
				amount, balance_before, balance_after, description, detail, category,
				reference_number, transaction_date, transaction_time, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT DO NOTHING`,
			uuid.New(), account.ID, m.Type,
			m.Amount, balBefore, balAfter, m.Desc, m.Detail, m.Category,
			refNum, dateStr, timeStr, now)
		if err != nil {
			log.Printf("  warn: insert mutation: %v", err)
		}
	}
	log.Printf("  mutations: %d seeded for %s", len(mutations), user.FullName)
}

func seedNotifications(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, now time.Time) {
	notifications := []struct {
		Title   string
		Body    string
		Type    string
		DaysAgo int
	}{
		{"Transfer Masuk", "Anda menerima transfer Rp 5.000.000 dari BUDI SANTOSO", "TRANSACTION", 0},
		{"Promo Cashback", "Dapatkan cashback 10% untuk transaksi QRIS di merchant favorit", "PROMO", 1},
		{"Keamanan Akun", "Login baru terdeteksi dari perangkat iPhone 15 Pro Max", "SECURITY", 2},
		{"Top Up Berhasil", "Top up GoPay Rp 500.000 berhasil diproses", "TRANSACTION", 1},
		{"Limit Harian", "Anda telah menggunakan 50% limit transfer harian", "INFO", 3},
	}

	for _, n := range notifications {
		createdAt := now.Add(-time.Duration(n.DaysAgo) * 24 * time.Hour)
		_, err := pool.Exec(ctx, `
			INSERT INTO notifications (id, user_id, title, body, type, is_read, created_at)
			VALUES ($1, $2, $3, $4, $5, false, $6)
			ON CONFLICT DO NOTHING`,
			uuid.New(), userID, n.Title, n.Body, n.Type, createdAt)
		if err != nil {
			log.Printf("  warn: insert notification: %v", err)
		}
	}
	log.Printf("  notifications: %d seeded", len(notifications))
}

func seedEWalletProviders(ctx context.Context, pool *pgxpool.Pool) {
	providers := []struct {
		ID           string
		Name         string
		MinAmount    int64
		MaxAmount    int64
		AdminFee     int64
		Presets      string
		SortOrder    int
	}{
		{"gopay", "GoPay", 10000, 2000000, 0, "[10000,20000,50000,100000,200000,500000]", 1},
		{"ovo", "OVO", 10000, 2000000, 0, "[10000,20000,50000,100000,200000,500000]", 2},
		{"dana", "DANA", 10000, 2000000, 0, "[10000,20000,50000,100000,200000,500000]", 3},
		{"shopeepay", "ShopeePay", 10000, 1000000, 1000, "[10000,20000,50000,100000,200000,500000]", 4},
		{"linkaja", "LinkAja", 10000, 2000000, 0, "[10000,25000,50000,100000,250000,500000]", 5},
	}

	for _, p := range providers {
		_, err := pool.Exec(ctx, `
			INSERT INTO ewallet_providers (id, name, is_active, min_amount, max_amount,
				admin_fee, preset_amounts, sort_order)
			VALUES ($1, $2, TRUE, $3, $4, $5, $6::jsonb, $7)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				min_amount = EXCLUDED.min_amount,
				max_amount = EXCLUDED.max_amount,
				admin_fee = EXCLUDED.admin_fee,
				preset_amounts = EXCLUDED.preset_amounts,
				sort_order = EXCLUDED.sort_order`,
			p.ID, p.Name, p.MinAmount, p.MaxAmount, p.AdminFee, p.Presets, p.SortOrder)
		if err != nil {
			log.Printf("  warn: insert ewallet provider %s: %v", p.Name, err)
		}
	}
	log.Printf("  ewallet providers: %d seeded", len(providers))
}

func seedPromotions(ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	promos := []struct {
		Title       string
		Description string
		ImageURL    string
	}{
		{
			"Cashback QRIS 10%",
			"Dapatkan cashback 10% setiap transaksi QRIS di merchant favorit. Maks cashback Rp 50.000/transaksi.",
			"/images/promo-qris-cashback.jpg",
		},
		{
			"Transfer Gratis Antar Bank",
			"Nikmati 5x transfer gratis antar bank setiap bulan untuk nasabah BCA Mobile.",
			"/images/promo-free-transfer.jpg",
		},
		{
			"Top Up E-Wallet Tanpa Biaya",
			"Top up GoPay, OVO, DANA tanpa biaya admin. Berlaku hingga akhir bulan.",
			"/images/promo-ewallet-free.jpg",
		},
	}

	for _, p := range promos {
		_, err := pool.Exec(ctx, `
			INSERT INTO promotions (id, title, description, image_url, is_active,
				start_date, end_date, created_at)
			VALUES ($1, $2, $3, $4, TRUE, $5, $6, $7)
			ON CONFLICT DO NOTHING`,
			uuid.New(), p.Title, p.Description, p.ImageURL,
			now.AddDate(0, 0, -7), now.AddDate(0, 1, 0), now)
		if err != nil {
			log.Printf("  warn: insert promo: %v", err)
		}
	}
	log.Printf("  promotions: %d seeded", len(promos))
}

// hashToken generates a SHA-256 hash for verification token seed data
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}