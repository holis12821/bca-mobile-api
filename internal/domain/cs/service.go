package cs

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/sms"
)

// Service melayani pencarian dan pembukaan profil nasabah oleh petugas CS.
type Service struct {
	customers CustomerRepository
	access    AccessLogRepository
	hasher    PhoneHasher

	clock func() time.Time
}

type ServiceConfig struct {
	Customers CustomerRepository
	Access    AccessLogRepository

	// Hasher menghitung phone_hash. Nil mematikan pencarian lewat nomor HP —
	// pencarian nomor rekening tetap jalan, karena nomor rekening tidak di-hash.
	Hasher PhoneHasher

	// Clock disuntik test. Nil memakai time.Now.
	Clock func() time.Time
}

func NewService(cfg ServiceConfig) *Service {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		customers: cfg.Customers,
		access:    cfg.Access,
		hasher:    cfg.Hasher,
		clock:     clock,
	}
}

// Search mencari nasabah dari satu kata kunci: nomor rekening atau nomor HP, COCOK PERSIS.
//
// Mengembalikan daftar kosong, bukan 404, kalau tidak ada yang cocok. Perbedaannya
// penting: 404 memberi tahu pemanggil bahwa kata kuncinya bukan nomor yang terdaftar,
// dan itu jawaban yang bisa dipakai menyapu ruang nomor rekening satu per satu.
//
// Setiap pencarian dicatat — termasuk yang tidak menemukan apa pun, karena pola
// pencarian yang gagal adalah justru yang paling perlu terlihat saat memeriksa
// penyalahgunaan.
func (s *Service) Search(ctx context.Context, query, agentEmployeeID, ip, userAgent string) ([]CustomerMatch, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, apperr.ValidationError
	}

	kinds := detectQueryKinds(query)
	if len(kinds) == 0 {
		// Bukan nomor rekening dan bukan nomor HP. Ditolak sebelum menyentuh database:
		// pencarian nama tidak didukung, dan membiarkannya menghasilkan daftar kosong
		// akan membuat pemanggil menyangka namanya memang tidak terdaftar.
		return nil, apperr.ValidationError
	}

	// Satu orang bisa cocok lewat dua jalur sekaligus pada kata kunci yang ambigu.
	seen := map[uuid.UUID]bool{}
	matches := make([]CustomerMatch, 0, 1)
	matchedKind := kinds[0]

	for _, kind := range kinds {
		found, err := s.searchBy(ctx, kind, query)
		if err != nil {
			return nil, err
		}
		for _, c := range found {
			if seen[c.UserID] {
				continue
			}
			seen[c.UserID] = true
			matches = append(matches, CustomerMatch{
				UserID:      c.UserID,
				FullName:    c.FullName,
				Tier:        c.Tier,
				Status:      c.Status,
				PhoneMasked: account.MaskPhone(c.Phone),
			})
		}
		if len(found) > 0 {
			matchedKind = kind
		}
	}

	// subject_user_id diisi hanya kalau hasilnya tepat SATU orang. Dengan lebih dari
	// satu, tidak ada satu subjek yang benar — dan memilih yang pertama akan membuat
	// jejak nasabah itu memuat pencarian yang juga menyentuh orang lain.
	var subject *uuid.UUID
	if len(matches) == 1 {
		id := matches[0].UserID
		subject = &id
	}

	s.writeAccess(ctx, &AccessLog{
		AgentEmployeeID: agentEmployeeID,
		Action:          ActionCustomerSearch,
		SubjectUserID:   subject,
		QueryKind:       matchedKind,
		ResultCount:     len(matches),
		IPAddress:       ip,
		UserAgent:       userAgent,
	})

	return matches, nil
}

// GetProfile membuka profil seorang nasabah, dengan penyamaran yang berlaku.
//
// Lihat [CustomerProfile] untuk kebijakan penyamarannya — termasuk alasan saldo tidak
// ada di dalamnya.
func (s *Service) GetProfile(ctx context.Context, userID uuid.UUID, agentEmployeeID, ip, userAgent string) (*CustomerProfile, error) {
	c, err := s.customers.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		// Dicatat TIDAK dilakukan di sini: nasabah yang tidak ada bukan data siapa pun
		// yang terbuka, dan menelusuri uuid acak tidak boleh memenuhi tabel jejak.
		return nil, apperr.NotFound
	}

	accounts, err := s.customers.ListAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}

	profile := &CustomerProfile{
		UserID:                  c.UserID,
		FullName:                c.FullName,
		DisplayName:             c.DisplayName,
		NIKMasked:               account.MaskNIK(c.NIK),
		PhoneMasked:             account.MaskPhone(c.Phone),
		EmailMasked:             account.MaskEmail(c.Email),
		Tier:                    c.Tier,
		Status:                  c.Status,
		BiometricEnabled:        c.BiometricEnabled,
		PushNotificationEnabled: c.PushNotificationEnabled,
		LockedUntil:             c.LockedUntil,
		LastLoginAt:             c.LastLoginAt,
		CreatedAt:               c.CreatedAt,
		Accounts:                make([]CustomerAccount, 0, len(accounts)),
	}

	for _, a := range accounts {
		profile.Accounts = append(profile.Accounts, CustomerAccount{
			AccountNumberMasked: account.MaskAccountNumber(a.AccountNumber),
			AccountType:         a.AccountType,
			AccountLabel:        a.AccountLabel,
			Currency:            a.Currency,
			IsPrimary:           a.IsPrimary,
			Status:              a.Status,
			OpenedAt:            a.OpenedAt,
			ClosedAt:            a.ClosedAt,
		})
	}

	id := c.UserID
	s.writeAccess(ctx, &AccessLog{
		AgentEmployeeID: agentEmployeeID,
		Action:          ActionCustomerViewed,
		SubjectUserID:   &id,
		ResultCount:     1,
		IPAddress:       ip,
		UserAgent:       userAgent,
	})

	return profile, nil
}

func (s *Service) searchBy(ctx context.Context, kind QueryKind, query string) ([]*Customer, error) {
	switch kind {
	case QueryAccountNumber:
		c, err := s.customers.FindByAccountNumber(ctx, digitsOnly(query))
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, nil
		}
		return []*Customer{c}, nil

	case QueryPhone:
		if s.hasher == nil {
			// Tanpa LOOKUP_HMAC_SECRET tidak ada cara mencocokkan phone_hash. Dilewati
			// dengan diam, bukan error: pencarian nomor rekening di permintaan yang sama
			// tetap sah, dan proses yang berjalan tanpa kunci itu sudah punya peringatan
			// boot sendiri.
			slog.Warn("cs phone search skipped: no lookup hasher configured")
			return nil, nil
		}
		candidates := phoneCandidates(query)
		if len(candidates) == 0 {
			return nil, nil
		}
		hashes := make([]string, 0, len(candidates))
		for _, cand := range candidates {
			hashes = append(hashes, s.hasher.Hash(cand))
		}
		return s.customers.FindByPhoneHashes(ctx, hashes)
	}
	return nil, nil
}

func (s *Service) writeAccess(ctx context.Context, log *AccessLog) {
	if s.access == nil {
		return
	}
	log.ID = uuid.New()
	log.CreatedAt = s.clock()

	// Kegagalan mencatat TIDAK menggagalkan permintaan: datanya sudah terbaca petugas
	// saat ini juga, dan menolak responsnya setelah itu tidak menarik kembali apa pun.
	// Yang penting kegagalannya berbunyi di log, bukan hilang.
	if err := s.access.Insert(ctx, log); err != nil {
		slog.Error("write cs access log failed",
			"agent_employee_id", log.AgentEmployeeID,
			"action", log.Action,
			"error", err)
	}
}

// detectQueryKinds menentukan jalur pencarian yang masuk akal untuk sebuah kata kunci.
//
// Bisa mengembalikan DUA pada kata kunci yang ambigu — nomor rekening 10 digit yang
// kebetulan berawalan 08 tidak bisa dibedakan dari nomor HP. Mencari keduanya lebih baik
// daripada memilih satu dan menjawab "tidak ditemukan" untuk orang yang jelas ada.
func detectQueryKinds(query string) []QueryKind {
	digits := digitsOnly(query)
	if digits == "" {
		return nil
	}

	var kinds []QueryKind

	// Nomor rekening m-BCA 10 digit. Hanya panjang persis, bukan rentang: rentang
	// membuat setiap nomor HP ikut dicari sebagai nomor rekening dan menggandakan
	// query untuk setiap pencarian.
	if len(digits) == 10 {
		kinds = append(kinds, QueryAccountNumber)
	}

	// Nomor HP divalidasi penormalisasi yang sama dengan jalur OTP, jadi "tidak ada
	// operator Indonesia yang bisa punya nomor ini" berarti hal yang sama di kedua tempat.
	if _, err := sms.NormalizePhone(query); err == nil {
		kinds = append(kinds, QueryPhone)
	}

	return kinds
}

// phoneCandidates menyusun bentuk-bentuk setara sebuah nomor yang mungkin tersimpan.
//
// phone_hash dihitung dari nomor APA ADANYA seperti diketik nasabah saat mendaftar —
// HMACHasher hanya memangkas spasi dan menurunkan huruf, tidak menormalisasi ke E.164.
// Jadi satu orang bisa tersimpan sebagai "08123…" sementara petugas mengetik "+62812…".
//
// KETERBATASAN YANG DIKETAHUI: nomor yang didaftarkan dengan pemisah — "0812-3456-7890"
// — menghasilkan hash yang tidak bisa disusun ulang dari masukan tanpa pemisah, jadi
// nasabah itu tidak akan ditemukan. Memperbaikinya menuntut menormalisasi phone_hash
// saat penulisan dan mengisi ulang yang sudah ada; itu pekerjaan tersendiri, bukan
// sesuatu yang bisa ditambal dari sisi pencarian.
func phoneCandidates(raw string) []string {
	e164, err := sms.NormalizePhone(raw)
	if err != nil {
		return nil
	}
	national := "0" + strings.TrimPrefix(e164, "+62")

	// Urutannya tidak penting — repository mencocokkan semuanya sekaligus. Yang penting
	// tidak ada duplikat, supaya daftar hash-nya tidak lebih panjang dari perlunya.
	forms := []string{raw, national, e164, strings.TrimPrefix(e164, "+")}

	seen := map[string]bool{}
	out := make([]string, 0, len(forms))
	for _, f := range forms {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
