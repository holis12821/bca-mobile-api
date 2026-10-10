package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
)

// CSDashboardRepo mengimplementasikan cs.DashboardRepository.
//
// Membaca onboarding_video_calls — tabel milik konteks onboarding — dan itu disengaja:
// angka yang ditampilkan dashboard petugas adalah akibat dari panggilan yang ia tangani,
// dan menyalinnya ke tabel ringkasan milik konteks cs akan membuat dua sumber yang bisa
// berbeda. Pembacaannya saja; tidak ada jalur tulis ke sini.
type CSDashboardRepo struct {
	pool *pgxpool.Pool
}

func NewCSDashboardRepo(pool *pgxpool.Pool) *CSDashboardRepo {
	return &CSDashboardRepo{pool: pool}
}

// AgentCallStats mengagregasi panggilan yang diselesaikan petugas itu dalam
// [since, until).
//
// Dibatasi ended_at, BUKAN joined_at: yang dihitung adalah pekerjaan yang ia selesaikan
// hari ini. Panggilan yang mulai 23:50 dan berakhir 00:10 masuk hitungan hari berikutnya,
// dan itu jawaban yang benar untuk pertanyaan "berapa yang sudah saya tangani hari ini".
//
// Rentangnya setengah terbuka ([since, until)) supaya panggilan tepat di tengah malam
// tidak terhitung dua kali di dua hari.
//
// Satu query, bukan empat: FILTER (WHERE ...) mengerjakan keempat pencacahan dalam satu
// pemindaian. Empat query untuk satu kartu di layar yang dimuat setiap petugas membuka
// beranda adalah empat perjalanan yang tidak perlu.
func (r *CSDashboardRepo) AgentCallStats(ctx context.Context, employeeID string, since, until time.Time) (*cs.DashboardCallStats, error) {
	var out cs.DashboardCallStats
	var avg *float64

	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE result = 'APPROVED'),
			count(*) FILTER (WHERE result = 'REJECTED'),
			count(*) FILTER (WHERE result = 'NEED_REVIEW'),
			-- NULLIF: panggilan yang tercatat 0 detik adalah panggilan yang durasinya
			-- tidak terlaporkan klien, bukan panggilan yang berlangsung nol detik.
			-- Memasukkannya ke rata-rata akan menariknya turun tanpa alasan.
			avg(NULLIF(call_duration_seconds, 0))
		FROM onboarding_video_calls
		WHERE agent_employee_id = $1
		  AND status = 'COMPLETED'
		  AND ended_at >= $2
		  AND ended_at <  $3`,
		employeeID, since, until,
	).Scan(&out.CallsHandled, &out.Approved, &out.Rejected, &out.NeedReview, &avg)
	if err != nil {
		return nil, fmt.Errorf("aggregate agent call stats: %w", err)
	}

	if avg != nil {
		// Dibulatkan ke detik terdekat: layar menampilkannya sebagai "3m 15s", dan
		// presisi di bawah detik tidak berarti apa pun di sana.
		out.AvgDurationSeconds = int(*avg + 0.5)
	}
	return &out, nil
}

// QueueSnapshot menghitung panggilan yang masih menunggu dan yang terlama menunggu.
//
// Angka ini untuk DITAMPILKAN, bukan untuk menentukan siapa dilayani berikutnya — itu
// tetap milik sorted set Redis. Dibaca dari Postgres supaya dashboard tidak ikut jatuh
// saat cache antrean bermasalah, dan supaya layar beranda tidak pernah menjadi alasan
// menyentuh jalur panas antrean.
func (r *CSDashboardRepo) QueueSnapshot(ctx context.Context, now time.Time) (*cs.DashboardQueueSnapshot, error) {
	var out cs.DashboardQueueSnapshot
	var longest *float64

	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			max(EXTRACT(EPOCH FROM ($1::timestamptz - joined_at)))
		FROM onboarding_video_calls
		WHERE status = 'QUEUED'`,
		now,
	).Scan(&out.Waiting, &longest)
	if err != nil {
		return nil, fmt.Errorf("snapshot video call queue: %w", err)
	}

	// `now` diberikan pemanggil, bukan now() di SQL: seluruh dashboard dirakit dari satu
	// stempel waktu, dan dua sumber waktu dalam satu layar menghasilkan angka yang tidak
	// pernah benar-benar konsisten.
	if longest != nil && *longest > 0 {
		out.LongestWaitSeconds = int(*longest)
	}
	return &out, nil
}
