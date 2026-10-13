package cs

import (
	"context"
	"log/slog"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// DashboardService merakit layar beranda petugas dari sumber-sumber yang sudah ada.
//
// Tidak punya tabel sendiri, dan itu disengaja: setiap angka di sini sudah dicatat di
// tempat lain sebagai akibat dari sebuah tindakan. Tabel ringkasan tersendiri akan
// menjadi salinan kedua yang bisa melenceng dari aslinya — dan yang melenceng akan
// dipercaya karena ia yang tampil di layar.
type DashboardService struct {
	calls     DashboardRepository
	sessions  AgentSessionRepository
	terminals TerminalRepository

	clock func() time.Time
}

// DashboardServiceConfig merakit DashboardService.
type DashboardServiceConfig struct {
	Calls     DashboardRepository
	Sessions  AgentSessionRepository
	Terminals TerminalRepository

	// Clock opsional; default time.Now. Diparameterkan supaya test bisa memaku batas
	// hari WIB — tanpa itu test yang dijalankan sesaat setelah tengah malam Jakarta akan
	// gagal untuk alasan yang tidak ada hubungannya dengan kodenya.
	Clock func() time.Time
}

func NewDashboardService(cfg DashboardServiceConfig) *DashboardService {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &DashboardService{
		calls:     cfg.Calls,
		sessions:  cfg.Sessions,
		terminals: cfg.Terminals,
		clock:     clock,
	}
}

// jakarta memuat zona WIB sekali per pemanggilan.
//
// Kegagalannya dijawab UTC, bukan error: batas hari yang bergeser tujuh jam lebih baik
// daripada layar beranda yang kosong, dan ia berbunyi di log supaya tidak diam-diam
// menjadi keadaan tetap.
func jakarta() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		slog.Error("load Asia/Jakarta failed; dashboard day boundary falls back to UTC",
			"error", err)
		return time.UTC
	}
	return loc
}

// Overview merakit seluruh layar dalam satu permintaan.
//
// `name` dan `scopes` datang dari middleware yang sudah memverifikasi petugasnya, bukan
// dari pencarian ulang ke database: verifikasinya sudah membacanya, dan membacanya lagi
// di sini berarti satu perjalanan tambahan untuk data yang sudah ada di tangan.
func (s *DashboardService) Overview(ctx context.Context, employeeID, name string, scopes []string) (*Dashboard, error) {
	now := s.clock()

	// Batas hari dihitung di APLIKASI, bukan CURRENT_DATE: server bisa berjalan di UTC,
	// dan hari kerja petugas berganti tengah malam Jakarta. Ini jebakan yang sudah
	// menggigit jalur limit harian di repo ini.
	loc := jakarta()
	local := now.In(loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	if scopes == nil {
		// Selalu array di JSON: klien yang menerima null akan memanggil
		// scopes.includes(...) pada nilai yang bukan array.
		scopes = []string{}
	}

	out := &Dashboard{
		Agent: DashboardAgent{
			EmployeeID: employeeID,
			Name:       name,
			Scopes:     scopes,
		},
		Queue: DashboardQueue{
			WithinOperatingHours: onboarding.WithinOperatingHours(now),
		},
		Measured:    measuredFields(),
		GeneratedAt: now,
	}

	// Giliran kerja. Ketiadaannya BUKAN kegagalan: petugas boleh membuka beranda dengan
	// kunci API sebelum ia masuk ke loket mana pun, dan justru itu yang membuatnya perlu
	// tahu bahwa ia belum masuk.
	if s.sessions != nil {
		sess, err := s.sessions.FindLiveByEmployee(ctx, employeeID)
		if err != nil {
			return nil, err
		}
		if sess != nil {
			started := sess.StartedAt
			out.Agent.Shift = sess.Shift
			out.Agent.SessionStartedAt = &started

			if s.terminals != nil && sess.TerminalID != "" {
				t, err := s.terminals.FindByID(ctx, sess.TerminalID)
				if err != nil {
					return nil, err
				}
				if t != nil {
					out.Terminal = &DashboardTerminal{
						TerminalID:  t.TerminalID,
						Workstation: t.Workstation,
						Location:    t.Location,
						Status:      t.Status,
						// Rule 4, sudah dihitung. Hanya ONLINE yang boleh mengambil
						// antrean, dan satu-satunya tempat lain yang menegakkannya adalah
						// AgentSignalingURL — keduanya harus sepakat.
						CanTakeCalls: t.Status == TerminalOnline,
					}
				}
			}
		}
	}

	if s.calls != nil {
		stats, err := s.calls.AgentCallStats(ctx, employeeID, dayStart, dayEnd)
		if err != nil {
			return nil, err
		}
		if stats != nil {
			out.Today = DashboardToday{
				CallsHandled:       stats.CallsHandled,
				Approved:           stats.Approved,
				Rejected:           stats.Rejected,
				NeedReview:         stats.NeedReview,
				AvgDurationSeconds: stats.AvgDurationSeconds,
			}
		}

		snap, err := s.calls.QueueSnapshot(ctx, now)
		if err != nil {
			return nil, err
		}
		if snap != nil {
			out.Queue.Waiting = snap.Waiting
			out.Queue.LongestWaitSeconds = snap.LongestWaitSeconds
		}
	}

	return out, nil
}

// measuredFields menyebut field mana yang benar-benar diukur.
//
// Satu tempat, supaya kontraknya tidak berbeda dari kenyataannya. Yang menghidupkan
// pengukur SLA nanti mengubah satu baris di sini — dan kalau ia lupa, layar akan tetap
// menyembunyikan angkanya, yang jauh lebih baik daripada menampilkan null sebagai 0%.
func measuredFields() map[string]bool {
	return map[string]bool{
		"calls_handled":        true,
		"approved":             true,
		"rejected":             true,
		"need_review":          true,
		"avg_duration_seconds": true,
		"queue_waiting":        true,
		"queue_longest_wait":   true,
		"terminal_status":      true,

		// Tidak ada yang mengukur ketiganya di sistem ini. Lihat catatan di [Dashboard].
		"sla_percent":  false,
		"csat_percent": false,
		"shift_target": false,
	}
}
