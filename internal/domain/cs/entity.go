// Package cs melayani petugas Customer Service Halo BCA: mencari nasabah yang sudah
// punya rekening, dan membuka profilnya.
//
// Dipisah dari paket `account` meski membaca tabel yang sama, karena pertanyaannya
// berbeda. `account` menjawab "apa yang boleh dilihat PEMILIK rekening"; paket ini
// menjawab "apa yang boleh dilihat ORANG LAIN yang sedang membantunya". Jawaban kedua
// selalu lebih sempit, dan menggabungkan keduanya dalam satu service membuat perluasan
// yang dimaksudkan untuk nasabah ikut terbuka untuk petugas tanpa ada yang menyadarinya.
package cs

import (
	"time"

	"github.com/google/uuid"
)

// QueryKind adalah jenis kunci pencarian yang dikenali.
//
// Dicatat ke jejak akses sebagai pengganti nilai pencariannya — lihat
// migrations/000029_cs_access_logs.
type QueryKind string

const (
	QueryAccountNumber QueryKind = "ACCOUNT_NUMBER"
	QueryPhone         QueryKind = "PHONE"
)

// Tindakan yang dicatat di cs_access_logs. Cocok dengan CHECK di migrasi 000029.
const (
	ActionCustomerSearch = "CUSTOMER_SEARCH"
	ActionCustomerViewed = "CUSTOMER_VIEWED"
)

// CustomerMatch adalah satu nasabah di hasil pencarian.
//
// Sengaja sangat sempit. Pencarian hanya perlu menjawab "apakah orang ini yang Anda
// maksud" supaya petugas bisa membuka profilnya; ia bukan tempat membaca datanya. Yang
// mau lebih memanggil profilnya, dan panggilan itu tercatat ke nasabah yang bersangkutan.
type CustomerMatch struct {
	UserID      uuid.UUID `json:"user_id"`
	FullName    string    `json:"full_name"`
	Tier        string    `json:"tier"`
	Status      string    `json:"status"`
	PhoneMasked string    `json:"phone_masked"`
}

// CustomerProfile adalah profil nasabah untuk petugas CS.
//
// KEBIJAKAN PENYAMARAN, dan alasan tiap keputusannya:
//
//   - full_name UTUH — memastikan petugas bicara dengan orang yang benar adalah inti
//     pekerjaannya.
//   - nik_masked, phone_masked, email_masked DISAMARKAN. Cukup untuk mencocokkan dengan
//     apa yang nasabah sebutkan sendiri di telepon, tidak cukup untuk menyamar sebagai
//     dia di tempat lain.
//   - SALDO TIDAK ADA DI SINI, dan itu keputusan sadar. Nasabah bisa melihat saldonya
//     sendiri di aplikasi; petugas tidak butuh angkanya untuk menyelesaikan keluhan, dan
//     daftar saldo seluruh nasabah adalah hal paling berharga yang bisa diambil dari
//     kredensial petugas yang bocor. Kalau suatu saat memang dibutuhkan, ia endpoint
//     tersendiri dengan cakupan tersendiri — bukan field tambahan di sini.
//   - Sakelar perangkat (biometric, push) ADA, karena "notifikasi saya tidak muncul"
//     adalah keluhan yang tidak bisa ditelusuri tanpanya.
type CustomerProfile struct {
	UserID      uuid.UUID `json:"user_id"`
	FullName    string    `json:"full_name"`
	DisplayName string    `json:"display_name"`
	NIKMasked   string    `json:"nik_masked"`
	PhoneMasked string    `json:"phone_masked"`
	EmailMasked string    `json:"email_masked"`
	Tier        string    `json:"tier"`
	Status      string    `json:"status"`

	BiometricEnabled        bool `json:"biometric_enabled"`
	PushNotificationEnabled bool `json:"push_notification_enabled"`

	// LockedUntil terisi hanya saat akun sedang terkunci karena PIN salah berulang.
	// Ini jawaban langsung untuk "kenapa saya tidak bisa masuk".
	LockedUntil *time.Time `json:"locked_until,omitempty"`

	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`

	Accounts []CustomerAccount `json:"accounts"`
}

// CustomerAccount adalah satu rekening milik nasabah, tanpa saldo.
type CustomerAccount struct {
	AccountNumberMasked string     `json:"account_number_masked"`
	AccountType         string     `json:"account_type"`
	AccountLabel        string     `json:"account_label"`
	Currency            string     `json:"currency"`
	IsPrimary           bool       `json:"is_primary"`
	Status              string     `json:"status"`
	OpenedAt            time.Time  `json:"opened_at"`
	ClosedAt            *time.Time `json:"closed_at,omitempty"`
}

// Customer adalah baris users apa adanya, sebelum penyamaran.
//
// Hanya dipakai antara repository dan service. Tidak pernah di-encode ke JSON — itu
// sebabnya tidak ada tag di sini: field-nya memuat PII utuh, dan satu `json.Marshal`
// yang tidak sengaja atas tipe ini akan mengirimkan semuanya.
type Customer struct {
	UserID      uuid.UUID
	FullName    string
	DisplayName string
	NIK         string
	Phone       string
	Email       string
	Tier        string
	Status      string

	BiometricEnabled        bool
	PushNotificationEnabled bool
	LockedUntil             *time.Time
	LastLoginAt             *time.Time
	CreatedAt               time.Time
}

// Account adalah baris accounts apa adanya.
type Account struct {
	AccountNumber string
	AccountType   string
	AccountLabel  string
	Currency      string
	IsPrimary     bool
	Status        string
	OpenedAt      time.Time
	ClosedAt      *time.Time
}

// AccessLog adalah satu baris cs_access_logs.
type AccessLog struct {
	ID              uuid.UUID
	AgentEmployeeID string
	Action          string

	// SubjectUserID nil untuk pencarian yang tidak menemukan apa pun.
	SubjectUserID *uuid.UUID

	// QueryKind kosong untuk tindakan yang bukan pencarian. Nilai pencariannya TIDAK
	// pernah disimpan.
	QueryKind QueryKind

	ResultCount int
	IPAddress   string
	UserAgent   string
	CreatedAt   time.Time
}
