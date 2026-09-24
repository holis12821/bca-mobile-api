package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

func main() {
	loadEnvFile(".env")

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

// loadEnvFile mirrors cmd/server: the seed must encrypt PII with the same
// AES_KEY and hash lookups with the same LOOKUP_HMAC_SECRET the API uses, or
// the rows it writes are unreadable to /account/profile.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// piiPassphrase is what pgp_sym_encrypt/pgp_sym_decrypt take as their key.
// It must be byte-identical to what ProfileRepo passes, or the API reads back
// rows it cannot decrypt: the hex text of AES_KEY, normalised to lower case
// exactly the way hex.EncodeToString emits it.
func piiPassphrase() string {
	hexKey := os.Getenv("AES_KEY")
	if hexKey == "" {
		log.Fatal("AES_KEY not set — copy .env.example to .env first")
	}

	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		log.Fatalf("AES_KEY is not valid hex: %v", err)
	}
	if len(raw) != 32 {
		log.Fatalf("AES_KEY must be 32 bytes (64 hex chars), got %d", len(raw))
	}

	return hex.EncodeToString(raw)
}

// stableID derives a deterministic UUID from the row's natural key, so
// re-running the seeder updates rows instead of piling up duplicates — a
// random uuid.New() here means every `make seed` doubles the mutation list.
func stableID(parts ...string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join(parts, "|")))
}

// displayName is the short, greeting-friendly form: "NURHOLIS MAJID" → "Nurholis".
func displayName(fullName string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(fullName), " ")
	if first == "" {
		return fullName
	}
	return strings.ToUpper(first[:1]) + strings.ToLower(first[1:])
}

// pinSaltFrom pulls the salt segment out of an Argon2id encoded hash
// ($argon2id$v=19$m=..,t=..,p=..$<salt>$<hash>). The column is legacy — the
// encoded hash already carries its own salt and VerifyPassword reads only that
// — but it is NOT NULL, so it needs a truthful value rather than a placeholder.
func pinSaltFrom(encoded string) string {
	parts := strings.Split(encoded, "$")
	if len(parts) < 5 {
		return "embedded"
	}
	return parts[4]
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

	passphrase := piiPassphrase()
	hasher := crypto.NewHMACHasher(os.Getenv("LOOKUP_HMAC_SECRET"))

	for _, u := range users {
		pinHash := hashPIN(u.PIN)

		// Insert user (upsert).
		// phone/email are stored encrypted (pgp_sym_encrypt, matching
		// ProfileRepo) with a separate HMAC column for lookups. Never a
		// plaintext phone_number column — that is what §4 forbids.
		_, err := pool.Exec(ctx, `
			INSERT INTO users (id, full_name, display_name, pin_hash, pin_salt,
				phone_encrypted, phone_hash, email_encrypted, email_hash,
				status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5,
				pgp_sym_encrypt($6, $7), $8, pgp_sym_encrypt($9, $7), $10,
				'ACTIVE', $11, $11)
			ON CONFLICT (id) DO UPDATE SET
				full_name = EXCLUDED.full_name,
				display_name = EXCLUDED.display_name,
				pin_hash = EXCLUDED.pin_hash,
				pin_salt = EXCLUDED.pin_salt,
				phone_encrypted = EXCLUDED.phone_encrypted,
				phone_hash = EXCLUDED.phone_hash,
				email_encrypted = EXCLUDED.email_encrypted,
				email_hash = EXCLUDED.email_hash,
				updated_at = EXCLUDED.updated_at`,
			u.ID, u.FullName, displayName(u.FullName), pinHash, pinSaltFrom(pinHash),
			u.Phone, passphrase, hasher.Hash(u.Phone), u.Email, hasher.Hash(u.Email),
			now)
		if err != nil {
			log.Fatalf("insert user %s: %v", u.FullName, err)
		}
		log.Printf("  user: %s (PIN: %s, device: %s)", u.FullName, u.PIN, u.DeviceID)

		// Insert device. No updated_at column here — devices are revoked,
		// never mutated in place.
		_, err = pool.Exec(ctx, `
			INSERT INTO devices (id, user_id, device_id, device_name, device_model, is_trusted, created_at)
			VALUES ($1, $2, $3, $4, $4, true, $5)
			ON CONFLICT DO NOTHING`,
			uuid.New(), u.ID, u.DeviceID, u.DeviceModel, now)
		if err != nil {
			log.Fatalf("insert device for %s: %v", u.FullName, err)
		}

		// Insert accounts. The first one is the primary — the dashboard
		// has to have something to lead with.
		//
		// The conflict target is account_number, not id: a dev database that
		// already carries a hand-made row for this number would otherwise
		// fail the unique constraint on every run. RETURNING id then gives
		// the row that actually exists, so mutations attach to it rather than
		// to an id the seeder merely hoped for.
		for i, a := range u.Accounts {
			var accountID uuid.UUID
			err = pool.QueryRow(ctx, `
				INSERT INTO accounts (id, user_id, account_number, account_type, account_label,
					balance, currency, status, is_primary, owner_type, created_at, updated_at)
				VALUES ($1, $2, $3, 'TAHAPAN', $4, $5, 'IDR', 'ACTIVE', $6, 'CUSTOMER', $7, $7)
				ON CONFLICT (account_number) DO UPDATE SET
					user_id = EXCLUDED.user_id,
					balance = EXCLUDED.balance,
					account_label = EXCLUDED.account_label,
					is_primary = EXCLUDED.is_primary,
					updated_at = EXCLUDED.updated_at
				RETURNING id`,
				a.ID, u.ID, a.AccountNumber, a.AccountLabel, a.Balance, i == 0, now,
			).Scan(&accountID)
			if err != nil {
				log.Fatalf("insert account %s: %v", a.AccountNumber, err)
			}
			u.Accounts[i].ID = accountID
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

	// Seed katalog kartu Paspor (development saja — lihat fungsinya)
	seedCardProducts(ctx, pool)
}

// seedCardProducts menanam katalog kartu untuk pengembangan lokal.
//
// ANGKA DI SINI PALSU DAN SENGAJA TERLIHAT PALSU. Biaya dan limit resmi belum
// diputuskan product owner — lihat docs/08-PILIH-KARTU-API-SPEC.md §17. Nilai
// berulang seperti 11111 / 2222222 dipilih supaya siapa pun yang melihatnya di
// layar atau di database langsung tahu itu bukan tarif sungguhan.
//
// Godaannya adalah memakai 14000/16000/19000 dari contoh §4, karena kelihatan
// lebih "nyata". Justru itu bahayanya: angka-angka itu berasal dari strings.xml
// client — data desain layar — dan kalau bocor ke staging tidak ada yang curiga.
//
// Fungsi ini menolak jalan di luar development. Katalog produksi diisi lewat
// admin API, bukan seeder.
func seedCardProducts(ctx context.Context, pool *pgxpool.Pool) {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if env == "" {
		env = "development"
	}
	if env != "development" && env != "test" {
		log.Printf("  card products: DILEWATI (APP_ENV=%s) — angka seed adalah placeholder dev", env)
		return
	}

	cards := []struct {
		Type          string
		Name          string
		Tier          string
		Style         string
		AdminFee      int64
		IssuanceFee   int64
		ReplaceFee    int64
		LimitCash     int64
		LimitBCA      int64
		LimitInterbnk int64
		LimitDebit    int64
		DeliveryMin   int
		DeliveryMax   int
		MinAge        int
		MinDeposit    int64
	}{
		{"PASPOR_BLUE", "Blue Mastercard", "DEBIT", "BLUE",
			11111, 0, 11111, 1111111, 11111111, 1111111, 11111111, 3, 7, 17, 500000},
		{"PASPOR_GOLD", "Gold Mastercard", "DEBIT", "GOLD",
			22222, 0, 22222, 2222222, 22222222, 2222222, 22222222, 3, 7, 17, 500000},
		{"PASPOR_PLATINUM", "Platinum Mastercard", "PLATINUM_DEBIT", "PLATINUM",
			33333, 0, 33333, 3333333, 33333333, 3333333, 33333333, 5, 10, 17, 500000},
	}

	for _, c := range cards {
		_, err := pool.Exec(ctx, `
			INSERT INTO card_products (card_type, name, tier_key, style,
				fee_monthly_admin, fee_card_issuance, fee_card_replacement,
				limit_cash_withdrawal, limit_transfer_bca,
				limit_transfer_interbank, limit_debit_purchase,
				delivery_days_min, delivery_days_max, min_age, min_initial_deposit)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (card_type) DO UPDATE SET
				name = EXCLUDED.name,
				tier_key = EXCLUDED.tier_key,
				style = EXCLUDED.style,
				fee_monthly_admin = EXCLUDED.fee_monthly_admin,
				fee_card_issuance = EXCLUDED.fee_card_issuance,
				fee_card_replacement = EXCLUDED.fee_card_replacement,
				limit_cash_withdrawal = EXCLUDED.limit_cash_withdrawal,
				limit_transfer_bca = EXCLUDED.limit_transfer_bca,
				limit_transfer_interbank = EXCLUDED.limit_transfer_interbank,
				limit_debit_purchase = EXCLUDED.limit_debit_purchase,
				delivery_days_min = EXCLUDED.delivery_days_min,
				delivery_days_max = EXCLUDED.delivery_days_max,
				min_age = EXCLUDED.min_age,
				min_initial_deposit = EXCLUDED.min_initial_deposit,
				updated_at = NOW()`,
			c.Type, c.Name, c.Tier, c.Style,
			c.AdminFee, c.IssuanceFee, c.ReplaceFee,
			c.LimitCash, c.LimitBCA, c.LimitInterbnk, c.LimitDebit,
			c.DeliveryMin, c.DeliveryMax, c.MinAge, c.MinDeposit)
		if err != nil {
			log.Printf("  warn: insert card product %s: %v", c.Type, err)
		}
	}

	// Pemetaan ke produk.
	//
	// Ketiga produk diisi supaya flow pilih kartu bisa dicoba pada semuanya di
	// development. Lineup di bawah adalah TEBAKAN untuk dev, bukan penawaran
	// resmi: Xpresi dan TabunganKu memang produk dengan tarif lebih rendah, jadi
	// keduanya diberi kartu kelas bawah saja. Jawaban resminya §17 butir 1 —
	// jangan menyalin daftar ini ke staging atau produksi.
	options := []struct {
		Product  string
		Card     string
		Order    int
		Default  bool
		Popular  bool
		BadgeKey string
	}{
		{"TAHAPAN_BCA", "PASPOR_BLUE", 1, true, true, "RECOMMENDED_BEGINNER"},
		{"TAHAPAN_BCA", "PASPOR_GOLD", 2, false, false, "FLEXIBLE_TRANSACTION"},
		{"TAHAPAN_BCA", "PASPOR_PLATINUM", 3, false, false, "MAX_LIMIT"},

		// Xpresi: dua kartu, Blue sebagai default.
		{"TAHAPAN_XPRESI", "PASPOR_BLUE", 1, true, true, "RECOMMENDED_BEGINNER"},
		{"TAHAPAN_XPRESI", "PASPOR_GOLD", 2, false, false, "FLEXIBLE_TRANSACTION"},

		// TabunganKu: satu kartu saja. Produk setoran awal Rp20 ribu tidak
		// masuk akal ditawari kartu dengan limit tertinggi.
		{"TABUNGANKU", "PASPOR_BLUE", 1, true, false, "RECOMMENDED_BEGINNER"},
	}

	for _, o := range options {
		_, err := pool.Exec(ctx, `
			INSERT INTO product_card_options (product_type, card_type, display_order,
				is_default, is_popular, badge_key)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (product_type, card_type, COALESCE(region_code, ''))
			DO UPDATE SET
				display_order = EXCLUDED.display_order,
				is_default = EXCLUDED.is_default,
				is_popular = EXCLUDED.is_popular,
				badge_key = EXCLUDED.badge_key,
				updated_at = NOW()`,
			o.Product, o.Card, o.Order, o.Default, o.Popular, o.BadgeKey)
		if err != nil {
			log.Printf("  warn: insert card option %s/%s: %v", o.Product, o.Card, err)
		}
	}

	log.Printf("  card products: %d seeded, %d options (ANGKA PLACEHOLDER DEV)",
		len(cards), len(options))
}

func seedMutations(ctx context.Context, pool *pgxpool.Pool, user seedUser, now time.Time) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	wibNow := now.In(loc)
	account := user.Accounts[0]

	mutations := []struct {
		Type     string
		Amount   decimal.Decimal
		Desc     string
		Detail   string
		Category string
		DaysAgo  int
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
			ON CONFLICT (id) DO NOTHING`,
			stableID("mutation", account.ID.String(), refNum, m.Desc), account.ID, m.Type,
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
			ON CONFLICT (id) DO NOTHING`,
			stableID("notification", userID.String(), n.Title), userID, n.Title, n.Body, n.Type, createdAt)
		if err != nil {
			log.Printf("  warn: insert notification: %v", err)
		}
	}
	log.Printf("  notifications: %d seeded", len(notifications))
}

func seedEWalletProviders(ctx context.Context, pool *pgxpool.Pool) {
	providers := []struct {
		ID        string
		Name      string
		MinAmount int64
		MaxAmount int64
		AdminFee  int64
		Presets   string
		SortOrder int
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
				valid_from, valid_until, created_at, updated_at)
			VALUES ($1, $2, $3, $4, TRUE, $5, $6, $7, $7)
			ON CONFLICT (id) DO NOTHING`,
			stableID("promotion", p.Title), p.Title, p.Description, p.ImageURL,
			now.AddDate(0, 0, -7), now.AddDate(0, 1, 0), now)
		if err != nil {
			log.Printf("  warn: insert promo: %v", err)
		}
	}
	log.Printf("  promotions: %d seeded", len(promos))
}
