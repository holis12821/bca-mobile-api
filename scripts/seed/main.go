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

	// Tier layanan. Kosong diperlakukan REGULER oleh tierOf — client
	// menyembunyikan badge untuk tier itu.
	Tier string

	// Cards kartu Paspor milik nasabah ini. Kosong berarti nasabah tanpa kartu,
	// dan itu keadaan yang memang perlu ada di data dev: GET /account/cards
	// wajib membalas array kosong untuk mereka, bukan 404.
	//
	// Daftar, bukan satu kartu: layar Profil Saya menampilkan semua kartu, dan
	// satu kartu per nasabah membuat jalur "kartu kedua", is_primary, dan kartu
	// terblokir tidak pernah tersentuh sampai ada nasabah sungguhan.
	Cards []seedCard
}

// seedCard adalah kartu milik nasabah, bukan katalog. Katalognya card_products.
type seedCard struct {
	ID           uuid.UUID
	CardType     string
	MaskedNumber string
	// ValidThruMonth saja yang ditetapkan; tahunnya dihitung dari waktu seed
	// supaya data dev tidak pelan-pelan menjadi kartu kedaluwarsa.
	ValidThruMonth int

	// AccountIndex menunjuk rekening mana di Accounts yang kartu ini menempel.
	// Kartu menempel pada rekening, bukan pada nasabah: satu orang bisa punya
	// beberapa rekening dengan kartu berbeda.
	AccountIndex int

	// Status dan BlockedReason ada supaya jalur kartu terblokir punya data
	// tanpa harus memblokir kartu lebih dulu lewat API — dan pemblokiran itu
	// tidak bisa dibatalkan dari aplikasi, jadi tester yang mencobanya akan
	// kehilangan satu-satunya kartu ACTIVE-nya.
	Status        string
	BlockedReason string

	IsPrimary            bool
	DebitOnlineEnabled   bool
	InternationalEnabled bool
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
			Tier:        "PRIORITAS",
			Cards: []seedCard{
				{
					ID:                   uuid.MustParse("00000000-0000-0000-0000-400000000001"),
					CardType:             "PASPOR_GOLD",
					MaskedNumber:         "•••• •••• •••• 7890",
					ValidThruMonth:       12,
					AccountIndex:         0,
					Status:               "ACTIVE",
					IsPrimary:            true,
					DebitOnlineEnabled:   true,
					InternationalEnabled: true,
				},
				{
					// Kartu kedua di rekening kedua: tanpa ini, urutan
					// is_primary dan tampilan daftar lebih dari satu kartu
					// tidak pernah terlihat di dev.
					ID:                 uuid.MustParse("00000000-0000-0000-0000-400000000003"),
					CardType:           "PASPOR_PLATINUM",
					MaskedNumber:       "•••• •••• •••• 1188",
					ValidThruMonth:     9,
					AccountIndex:       1,
					Status:             "ACTIVE",
					DebitOnlineEnabled: true,
					// Transaksi luar negeri mati sampai nasabah menyalakannya
					// sendiri — default yang sama dengan migrasi 000021.
					InternationalEnabled: false,
				},
			},
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
			// Tier kosong = REGULER. Badge tier harus hilang untuk nasabah ini.
			Cards: []seedCard{
				{
					ID:                 uuid.MustParse("00000000-0000-0000-0000-400000000002"),
					CardType:           "PASPOR_BLUE",
					MaskedNumber:       "•••• •••• •••• 3210",
					ValidThruMonth:     8,
					AccountIndex:       0,
					Status:             "ACTIVE",
					IsPrimary:          true,
					DebitOnlineEnabled: true,
				},
				{
					// Satu kartu TERBLOKIR di data dev. Client punya tampilan
					// sendiri untuk status ini, dan memblokir kartu lewat API
					// untuk mengujinya tidak bisa dibatalkan dari aplikasi.
					ID:             uuid.MustParse("00000000-0000-0000-0000-400000000004"),
					CardType:       "PASPOR_BLUE",
					MaskedNumber:   "•••• •••• •••• 4471",
					ValidThruMonth: 3,
					AccountIndex:   0,
					Status:         "BLOCKED",
					BlockedReason:  "LOST",
				},
			},
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
			Tier:        "SOLITAIRE",
			// Cards sengaja kosong: satu nasabah tanpa kartu supaya empty state
			// GET /account/cards bisa dicoba tanpa mengubah data.
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
				status, tier, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5,
				pgp_sym_encrypt($6, $7), $8, pgp_sym_encrypt($9, $7), $10,
				'ACTIVE', $12, $11, $11)
			ON CONFLICT (id) DO UPDATE SET
				full_name = EXCLUDED.full_name,
				display_name = EXCLUDED.display_name,
				pin_hash = EXCLUDED.pin_hash,
				pin_salt = EXCLUDED.pin_salt,
				phone_encrypted = EXCLUDED.phone_encrypted,
				phone_hash = EXCLUDED.phone_hash,
				email_encrypted = EXCLUDED.email_encrypted,
				email_hash = EXCLUDED.email_hash,
				tier = EXCLUDED.tier,
				updated_at = EXCLUDED.updated_at`,
			u.ID, u.FullName, displayName(u.FullName), pinHash, pinSaltFrom(pinHash),
			u.Phone, passphrase, hasher.Hash(u.Phone), u.Email, hasher.Hash(u.Email),
			now, tierOf(u))
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
		//
		// balance is the one column this upsert does NOT force back to the seed
		// constant. Resetting it used to leave `make ledger-check` reporting a
		// permanent violation: a re-seed rewound accounts.balance while
		// account_mutations and the ledger entries written by real transfers
		// stayed where they were, so v_ledger_reconciliation reported a drift
		// exactly equal to every transfer made since the previous seed. The
		// money invariant check then failed for a reason that had nothing to do
		// with the money code — the worst kind of broken check, because people
		// learn to ignore it.
		//
		// An account that has never moved still gets the seed balance, so a
		// fresh database looks exactly as before.
		for i, a := range u.Accounts {
			var accountID uuid.UUID
			var balance decimal.Decimal
			err = pool.QueryRow(ctx, `
				INSERT INTO accounts (id, user_id, account_number, account_type, account_label,
					balance, currency, status, is_primary, owner_type, created_at, updated_at)
				VALUES ($1, $2, $3, 'TAHAPAN', $4, $5, 'IDR', 'ACTIVE', $6, 'CUSTOMER', $7, $7)
				ON CONFLICT (account_number) DO UPDATE SET
					user_id = EXCLUDED.user_id,
					balance = CASE
						WHEN EXISTS (SELECT 1 FROM account_mutations m WHERE m.account_id = accounts.id)
						THEN accounts.balance
						ELSE EXCLUDED.balance
					END,
					account_label = EXCLUDED.account_label,
					is_primary = EXCLUDED.is_primary,
					updated_at = EXCLUDED.updated_at
				RETURNING id, balance`,
				a.ID, u.ID, a.AccountNumber, a.AccountLabel, a.Balance, i == 0, now,
			).Scan(&accountID, &balance)
			if err != nil {
				log.Fatalf("insert account %s: %v", a.AccountNumber, err)
			}
			u.Accounts[i].ID = accountID
			if balance.Equal(a.Balance) {
				log.Printf("    account: %s (%s) balance: %s", a.AccountNumber, a.AccountLabel, balance.String())
			} else {
				log.Printf("    account: %s (%s) balance: %s (dipertahankan — akun punya riwayat mutasi, saldo seed %s tidak dipakai)",
					a.AccountNumber, a.AccountLabel, balance.String(), a.Balance.String())
			}
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

	// Katalog kartu Paspor datang dari migrasi 000022, bukan dari seeder.
	// Di sini hanya diperiksa supaya kegagalan di bawah punya sebab yang jelas.
	verifyCardCatalog(ctx, pool)

	// Kartu milik nasabah butuh katalog sudah ada: account_cards.card_type punya
	// foreign key ke card_products. Kalau migrasi 000022 belum dijalankan, baris
	// log di verifyCardCatalog yang menjelaskannya.
	seedAccountCards(ctx, pool, users, now)

	// Petugas CS untuk menguji video call e-KYC dari ujung ke ujung.
	seedCSAgents(ctx, pool, hashPIN)

	// Terminal, supervisor, dan kata sandi petugas: tanpa ketiganya alur desktop CS
	// (login → kesiapan → aktivasi) tidak bisa dijalankan sekali pun secara lokal,
	// karena tidak ada endpoint yang membuat baris supervisor.
	seedCSWorkstations(ctx, pool, hashPIN)
}

// seedCSAgents menanam satu petugas CS untuk pengujian lokal video call.
//
// DIGERBANGI APP_ENV, dan itu bukan formalitas: isinya kredensial, bukan data referensi.
// Seeder ini tidak punya gerbang environment sendiri — ia menanam nasabah ber-PIN 123456
// ke database mana pun yang ditunjuk DATABASE_URL — jadi tanpa gerbang di sini satu kali
// `make seed` yang salah arah akan membuat kunci petugas yang diketahui umum bisa dipakai
// mengambil panggilan dan menandatangani hasil verifikasi identitas.
//
// Di luar development, baris cs_agents dibuat oleh yang mengoperasikan integrasi CS,
// dengan kunci acak, lewat jalur yang sama dengan pendistribusian INTERNAL_API_KEY.
func seedCSAgents(ctx context.Context, pool *pgxpool.Pool, hash func(string) string) {
	if env := os.Getenv("APP_ENV"); env != "development" {
		log.Printf("cs_agents dilewati: APP_ENV=%q, bukan development", env)
		return
	}

	// Dua petugas dengan cakupan berbeda, bukan satu yang memegang keduanya: pemisahan
	// kewenangan yang tidak pernah diuji terpisah akan terlihat berfungsi sampai orang
	// pertama yang hanya punya satu scope mencobanya.
	agents := []struct {
		employeeID string
		name       string
		apiKey     string
		scopes     []string
	}{
		{"CS-1042", "Sarah Adisti", "dev-agent-key", []string{"VIDEO_CALL"}},
		{"OPS-2001", "Budi Hartono", "dev-cardadmin-key", []string{"CARD_ADMIN"}},

		// Penyelia memegang beberapa cakupan sekaligus. Ada di seed supaya jalur
		// multi-scope ikut terlatih: petugas satu-cakupan tidak pernah membuktikan
		// bahwa pemeriksaannya benar untuk yang memegang lebih dari satu.
		//
		// AUDIT_READ dan ESCALATION_REVIEW (migrasi 000041) hanya di sini, bukan di
		// CS-1042: keduanya kewenangan pengawas. AUDIT_READ membuka jejak REKAN
		// SEKERJA, dan ESCALATION_REVIEW menutup perkara yang Tier 1 ajukan — memberi
		// keduanya kepada petugas panggilan akan membuat gerbangnya ada tapi tidak
		// memisahkan apa pun.
		{"SPV-3001", "Rina Kusuma", "dev-spv-key",
			[]string{"VIDEO_CALL", "CUSTOMER_PII", "TICKET", "AUDIT_READ", "ESCALATION_REVIEW"}},

		// Peninjau Tier 2 KEDUA, tanpa VIDEO_CALL. Dua alasan ia ada:
		//
		//   - Four-eyes hanya bisa diuji dengan dua orang. Perkara yang diajukan CS-1042
		//     ditutup SPV-3001; perkara yang diajukan SPV-3001 ditutup yang ini.
		//   - ESCALATION_CLAIMED_BY_OTHER mustahil dipicu oleh satu peninjau.
		//
		// Tanpa VIDEO_CALL dengan sengaja: ia membuktikan bahwa menutup perkara TIDAK
		// menuntut kewenangan mengambil panggilan.
		{"SPV-3002", "Dewi Lestari", "dev-tier2-key",
			[]string{"CUSTOMER_PII", "ESCALATION_REVIEW"}},
	}

	for _, a := range agents {
		_, err := pool.Exec(ctx, `
			INSERT INTO cs_agents (employee_id, name, api_key_hash, scopes, is_active)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (employee_id) DO UPDATE
			SET name = EXCLUDED.name,
			    api_key_hash = EXCLUDED.api_key_hash,
			    scopes = EXCLUDED.scopes,
			    is_active = true,
			    updated_at = now()`,
			a.employeeID, a.name, hash(a.apiKey), a.scopes,
		)
		if err != nil {
			log.Fatalf("seed cs_agents: %v", err)
		}
		log.Printf("cs agent: %s (%s), scopes=%v, X-Agent-API-Key: %s",
			a.employeeID, a.name, a.scopes, a.apiKey)
	}
}

// tierOf memetakan tier kosong ke REGULER. Kolom users.tier NOT NULL, dan
// REGULER adalah default skema — bukan tier istimewa.
func tierOf(u seedUser) string {
	if u.Tier == "" {
		return "REGULER"
	}
	return u.Tier
}

// verifyCardCatalog memeriksa katalog kartu, tidak menanamnya.
//
// Katalog dulu ditanam di sini dengan angka yang sengaja palsu (11111 / 22222 /
// 33333), digerbangi APP_ENV supaya tidak bocor ke staging. Akibatnya staging
// tidak pernah punya katalog sama sekali, dan angka kartu hidup di dua tempat.
//
// Sekarang katalog adalah data referensi yang dibawa migrasi
// 000022_card_catalog_rates: satu sumber angka, ikut ke setiap environment,
// dan perubahan tarif berikutnya lewat admin API — bukan lewat seeder.
//
// Yang tersisa di sini hanya penjaga: kalau katalognya kosong, seedAccountCards
// di bawah akan gagal dengan pelanggaran foreign key, dan pesan itu jauh lebih
// sulit dibaca daripada baris log ini.
func verifyCardCatalog(ctx context.Context, pool *pgxpool.Pool) {
	var cards, options int
	var version string

	err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM card_products WHERE is_active),
		       (SELECT count(*) FROM product_card_options),
		       (SELECT version_date || '.' || counter FROM card_catalog_version)`,
	).Scan(&cards, &options, &version)
	if err != nil {
		log.Printf("  warn: baca katalog kartu: %v", err)
		return
	}

	if cards == 0 {
		log.Printf("  card catalog: KOSONG — jalankan `make migrate-up` (migrasi 000022). " +
			"Kartu milik nasabah di bawah akan gagal karena foreign key ke card_products.")
		return
	}

	log.Printf("  card catalog: %d kartu aktif, %d opsi produk, versi %s "+
		"(dari migrasi 000022 — angka portofolio, bukan tarif resmi BCA)",
		cards, options, version)
}

// seedAccountCards menanam kartu MILIK nasabah — bukan katalog.
//
// Terikat gerbang environment yang sama dengan seedCardProducts, dan bukan demi
// konsistensi belaka: account_cards.card_type merujuk card_products, yang hanya
// terisi di development. Di luar dev, kartu nasabah datang dari core banking,
// bukan dari seeder.
//
// Nomor kartu di sini tersamar, dan itu bukan pilihan gaya: constraint
// account_card_number_masked (migrasi 000021) menolak baris yang memuat tujuh
// angka berurutan. Menulis PAN lengkap di seeder akan GAGAL di database, tidak
// lolos diam-diam.
func seedAccountCards(ctx context.Context, pool *pgxpool.Pool, users []seedUser, now time.Time) {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if env == "" {
		env = "development"
	}
	if env != "development" && env != "test" {
		log.Printf("  account cards: DILEWATI (APP_ENV=%s) — kartu nasabah berasal dari core banking", env)
		return
	}

	// Kartu dev berlaku tiga tahun dari waktu seed. Tahun yang dipatok akan
	// pelan-pelan berubah menjadi kartu kedaluwarsa dan menutupi jalur ACTIVE
	// yang justru paling sering diuji.
	validYear := now.Year() + 3

	seeded := 0
	withoutCards := 0
	for _, u := range users {
		if len(u.Cards) == 0 {
			withoutCards++
			continue
		}
		if len(u.Accounts) == 0 {
			log.Printf("  warn: %s punya kartu tapi tidak punya rekening — dilewati", u.FullName)
			continue
		}

		for _, c := range u.Cards {
			// Rekening yang ditunjuk kartu. ID-nya sudah ditulis balik dari
			// RETURNING id, jadi kartu menempel pada baris yang BENAR-BENAR
			// ada — bukan pada id yang seeder harapkan.
			idx := c.AccountIndex
			if idx < 0 || idx >= len(u.Accounts) {
				log.Printf("  warn: kartu %s menunjuk rekening ke-%d yang tidak ada pada %s — dipasang ke rekening utama",
					c.MaskedNumber, idx, u.FullName)
				idx = 0
			}
			accountID := u.Accounts[idx].ID

			status := c.Status
			if status == "" {
				status = "ACTIVE"
			}

			// blocked_reason dan blocked_at hanya terisi untuk kartu terblokir.
			// Menulis alasan pada kartu aktif akan lolos constraint tapi
			// membuat response memuat alasan blokir untuk kartu yang hidup.
			var blockedReason *string
			var blockedAt *time.Time
			if status == "BLOCKED" {
				reason := c.BlockedReason
				if reason == "" {
					reason = "LOST"
				}
				blockedReason = &reason
				blockedTime := now.AddDate(0, 0, -2)
				blockedAt = &blockedTime
			}

			_, err := pool.Exec(ctx, `
				INSERT INTO account_cards (id, user_id, account_id, card_type,
					masked_number, cardholder_name, valid_thru_month, valid_thru_year,
					status, blocked_reason, blocked_at,
					debit_online_enabled, international_enabled, is_primary,
					created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
					$9, $10, $11, $12, $13, $14, $15, $15)
				ON CONFLICT (id) DO UPDATE SET
					account_id = EXCLUDED.account_id,
					card_type = EXCLUDED.card_type,
					masked_number = EXCLUDED.masked_number,
					cardholder_name = EXCLUDED.cardholder_name,
					valid_thru_month = EXCLUDED.valid_thru_month,
					valid_thru_year = EXCLUDED.valid_thru_year,
					status = EXCLUDED.status,
					blocked_reason = EXCLUDED.blocked_reason,
					blocked_at = EXCLUDED.blocked_at,
					debit_online_enabled = EXCLUDED.debit_online_enabled,
					international_enabled = EXCLUDED.international_enabled,
					is_primary = EXCLUDED.is_primary,
					updated_at = EXCLUDED.updated_at`,
				c.ID, u.ID, accountID, c.CardType,
				c.MaskedNumber, u.FullName, c.ValidThruMonth, validYear,
				status, blockedReason, blockedAt,
				c.DebitOnlineEnabled, c.InternationalEnabled, c.IsPrimary,
				now)
			if err != nil {
				log.Fatalf("insert account card for %s: %v", u.FullName, err)
			}
			seeded++
			log.Printf("    card: %s %s (%s) %s valid thru %02d/%d",
				u.FullName, c.MaskedNumber, c.CardType, status, c.ValidThruMonth, validYear)
		}
	}

	log.Printf("  account cards: %d kartu untuk %d nasabah, %d nasabah sengaja tanpa kartu",
		seeded, len(users)-withoutCards, withoutCards)
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

// seedCSWorkstations menanam loket, supervisor, dan kata sandi petugas untuk alur desktop CS.
//
// Digerbangi APP_ENV dengan alasan yang sama dengan seedCSAgents, dan satu alasan
// tambahan: token dual-control supervisor adalah kredensial yang MENANDATANGANI kesiapan
// orang lain. Token yang diketahui umum membuat otorisasi dua-orang menjadi satu orang.
//
// Dibutuhkan karena `cs_supervisors` tidak punya endpoint pembuat — satu-satunya jalan
// masuknya adalah seed atau SQL manual, jadi tanpa ini layar SCR-006 tidak bisa dilewati
// di lingkungan lokal.
func seedCSWorkstations(ctx context.Context, pool *pgxpool.Pool, hash func(string) string) {
	if env := os.Getenv("APP_ENV"); env != "development" {
		log.Printf("cs_terminals/cs_supervisors dilewati: APP_ENV=%q, bukan development", env)
		return
	}

	terminals := []struct {
		terminalID  string
		workstation string
		location    string
	}{
		{"WKS-SMG-0842", "Loket 4", "KCU Semarang"},
		{"WKS-JKT-0117", "Loket 1", "KCU Jakarta Thamrin"},
	}

	for _, t := range terminals {
		// registered_by menunjuk petugas yang ada: kolomnya NOT NULL dan jejak audit
		// pendaftaran terminal harus bisa menyebut seseorang.
		_, err := pool.Exec(ctx, `
			INSERT INTO cs_terminals (terminal_id, workstation, location, status, registered_by)
			VALUES ($1, $2, $3, 'REGISTERED', 'OPS-2001')
			ON CONFLICT (terminal_id) DO UPDATE
			SET workstation = EXCLUDED.workstation,
			    location    = EXCLUDED.location,
			    updated_at  = now()`,
			t.terminalID, t.workstation, t.location,
		)
		if err != nil {
			log.Fatalf("seed cs_terminals: %v", err)
		}
		log.Printf("cs terminal: %s (%s, %s) REGISTERED", t.terminalID, t.workstation, t.location)
	}

	supervisors := []struct {
		supervisorID string
		name         string
		location     string
		token        string
	}{
		{"SPV-0021", "Budi Hartono", "KCU Semarang", "123456"},
		{"SPV-0022", "Rina Kusuma", "KCU Jakarta Thamrin", "654321"},
	}

	for _, sv := range supervisors {
		_, err := pool.Exec(ctx, `
			INSERT INTO cs_supervisors (supervisor_id, name, location, token_hash, is_active)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (supervisor_id) DO UPDATE
			SET name       = EXCLUDED.name,
			    location   = EXCLUDED.location,
			    token_hash = EXCLUDED.token_hash,
			    is_active  = true,
			    updated_at = now()`,
			sv.supervisorID, sv.name, sv.location, hash(sv.token),
		)
		if err != nil {
			log.Fatalf("seed cs_supervisors: %v", err)
		}
		log.Printf("cs supervisor: %s (%s, %s), token dual-control: %s",
			sv.supervisorID, sv.name, sv.location, sv.token)
	}

	// Kata sandi login petugas. Minimal 12 karakter — aturan yang sama dengan
	// AGENT_PASSWORD_WEAK di jalur `POST /internal/v1/auth/password`; kata sandi seed yang
	// lebih pendek akan membuat seed menanam baris yang endpoint-nya sendiri menolak.
	const devAgentPassword = "KataSandiPanjang2026"

	for _, employeeID := range []string{"CS-1042", "OPS-2001", "SPV-3001", "SPV-3002"} {
		_, err := pool.Exec(ctx, `
			UPDATE cs_agents
			SET password_hash         = $2,
			    password_set_at       = now(),
			    failed_login_attempts = 0,
			    locked_until          = NULL,
			    updated_at            = now()
			WHERE employee_id = $1`,
			employeeID, hash(devAgentPassword),
		)
		if err != nil {
			log.Fatalf("seed cs agent password: %v", err)
		}
	}
	log.Printf("cs agent password (CS-1042, OPS-2001, SPV-3001, SPV-3002): %s", devAgentPassword)
}
