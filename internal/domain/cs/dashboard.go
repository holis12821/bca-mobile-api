package cs

import (
	"context"
	"time"
)

// Dashboard adalah jawaban `GET /internal/v1/cs/dashboard` — layar beranda petugas
// (SCR-010).
//
// KEPUTUSAN yang menentukan bentuknya: dokumen alur menyebut `SLA 98.4%`,
// `CSAT 96.8%`, dan `Target Shift 50`. Ketiganya angka demo (§75 dokumen itu) — tidak
// ada definisi SLA yang disepakati, tidak ada mekanisme pengukuran kepuasan nasabah, dan
// tidak ada target shift yang ditetapkan siapa pun.
//
// Ketiganya tetap ADA di kontrak ini, bernilai `null`, dan `measured` menyebutkan mana
// yang benar-benar terukur. Alasannya dua arah: aplikasi desktop tidak perlu berubah
// bentuk saat pengukurnya nanti ada, dan sementara itu tidak ada angka karangan yang
// dipakai menilai orang. KPI karangan di layar operasional akan dipercaya.
//
// Yang TIDAK dilakukan: mengirim 0 sebagai ganti null. Nol terbaca sebagai "SLA-nya nol",
// dan itu kebohongan yang berbeda, bukan ketiadaan data.
type Dashboard struct {
	Agent    DashboardAgent     `json:"agent"`
	Today    DashboardToday     `json:"today"`
	Queue    DashboardQueue     `json:"queue"`
	Terminal *DashboardTerminal `json:"terminal"`

	// Measured memetakan nama field ke apakah ada yang mengukurnya. Klien yang menemukan
	// `false` menyembunyikan kartunya, bukan menampilkan null sebagai 0%.
	Measured map[string]bool `json:"measured"`

	// GeneratedAt stempel saat angkanya dihitung, bukan saat klien menerimanya.
	GeneratedAt time.Time `json:"generated_at"`
}

// DashboardAgent adalah petugas yang membuka layar ini, dan giliran kerjanya.
type DashboardAgent struct {
	EmployeeID string   `json:"employee_id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`

	// Shift dan SessionStartedAt nil kalau petugasnya belum membuka giliran di loket
	// mana pun — keadaan sah: `auth/me` dan dashboard sama-sama bisa dipanggil dengan
	// kunci API sebelum login.
	Shift            string     `json:"shift,omitempty"`
	SessionStartedAt *time.Time `json:"session_started_at"`
}

// DashboardToday adalah angka hari ini, dibatasi tengah malam WIB.
//
// Batas harinya dihitung APLIKASI, bukan `CURRENT_DATE`: server bisa berjalan di UTC,
// dan hari kerja petugas berganti tengah malam Jakarta.
type DashboardToday struct {
	CallsHandled int `json:"calls_handled"`
	Approved     int `json:"approved"`
	Rejected     int `json:"rejected"`
	NeedReview   int `json:"need_review"`

	// AvgDurationSeconds 0 kalau belum ada panggilan selesai hari ini. Nol di sini tidak
	// ambigu — nol panggilan memang berarti nol rata-rata.
	AvgDurationSeconds int `json:"avg_duration_seconds"`

	// SLAPercent, CSATPercent, ShiftTarget: lihat catatan di [Dashboard]. Selalu nil
	// sampai ada yang mengukurnya.
	SLAPercent  *float64 `json:"sla_percent"`
	CSATPercent *float64 `json:"csat_percent"`
	ShiftTarget *int     `json:"shift_target"`
}

// DashboardQueue adalah keadaan antrean saat ini — milik seluruh cabang, bukan petugas
// ini saja.
type DashboardQueue struct {
	Waiting int `json:"waiting"`

	// LongestWaitSeconds menjawab pertanyaan yang `waiting` tidak jawab: antrean 3 orang
	// yang terlama menunggu 40 menit adalah keadaan yang berbeda dari antrean 3 orang
	// yang baru datang.
	LongestWaitSeconds int `json:"longest_wait_seconds"`

	WithinOperatingHours bool `json:"within_operating_hours"`
}

// DashboardTerminal adalah loket petugas ini. Nil kalau ia belum masuk ke loket mana pun.
type DashboardTerminal struct {
	TerminalID  string         `json:"terminal_id"`
	Workstation string         `json:"workstation"`
	Location    string         `json:"location"`
	Status      TerminalStatus `json:"status"`

	// CanTakeCalls adalah Rule 4 yang sudah dihitung: hanya terminal ONLINE yang boleh
	// mengambil antrean. Dikirim server supaya tombol "Ambil Panggilan" tidak perlu
	// menyimpulkannya dari string status — dan supaya klien tidak bisa menyimpulkan
	// sebaliknya.
	CanTakeCalls bool `json:"can_take_calls"`
}

// DashboardCallStats adalah hasil agregat satu petugas dalam satu rentang waktu.
type DashboardCallStats struct {
	CallsHandled int
	Approved     int
	Rejected     int
	NeedReview   int

	// AvgDurationSeconds dibulatkan ke detik terdekat. Presisi di bawah itu tidak
	// berarti apa pun bagi layar yang menampilkannya sebagai "3m 15s".
	AvgDurationSeconds int
}

// DashboardQueueSnapshot adalah keadaan antrean saat dibaca.
type DashboardQueueSnapshot struct {
	Waiting            int
	LongestWaitSeconds int
}

// DashboardRepository membaca angka yang tidak dimiliki repo lain.
//
// Dua metode, bukan satu per kartu: layar ini dimuat setiap petugas membuka beranda, dan
// setiap metode tambahan adalah satu perjalanan ke database lagi untuk satu layar.
type DashboardRepository interface {
	// AgentCallStats mengagregasi panggilan yang DISELESAIKAN petugas itu dalam
	// [since, until). Rentangnya diberikan pemanggil, bukan dihitung SQL: batas hari WIB
	// adalah keputusan aplikasi.
	AgentCallStats(ctx context.Context, employeeID string, since, until time.Time) (*DashboardCallStats, error)

	// QueueSnapshot menghitung panggilan yang masih QUEUED dan yang terlama menunggu.
	//
	// Dari Postgres, bukan dari sorted set Redis yang dipakai antrean: angka ini untuk
	// ditampilkan, bukan untuk menentukan siapa dilayani berikutnya, dan membacanya dari
	// Redis akan membuat dashboard ikut jatuh saat cache antrean bermasalah.
	QueueSnapshot(ctx context.Context, now time.Time) (*DashboardQueueSnapshot, error)
}
