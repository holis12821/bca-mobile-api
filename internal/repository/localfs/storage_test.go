package localfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorage_RoundTrip(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	payload := []byte("foto e-ktp terenkripsi")

	path, err := s.Upload(ctx, "onboarding-ktp", "onb_123/photo.jpg", payload, "application/octet-stream")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if path != "local://onboarding-ktp/onb_123/photo.jpg" {
		t.Fatalf("path tidak terduga: %q", path)
	}

	got, err := s.Download(ctx, "onboarding-ktp", "onb_123/photo.jpg")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("isi berbeda: %q", got)
	}
}

// Ini yang paling penting: inilah yang tidak dilakukan MockObjectStorage, dan
// karena itu face match biometrik tidak pernah punya pembanding.
func TestStorage_UploadedObjectIsReadableBack(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	ctx := context.Background()

	path, err := s.Upload(ctx, "onboarding-ktp", "onb_1/a.jpg", []byte("abc"), "")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	bucket, key, ok := ParseObjectPath(path)
	if !ok {
		t.Fatalf("path tidak bisa dipecah: %q", path)
	}

	got, err := s.Download(ctx, bucket, key)
	if err != nil || string(got) != "abc" {
		t.Fatalf("objek tidak terbaca kembali: %q, err %v", got, err)
	}

	// Dan berkasnya benar-benar ada di disk.
	if _, err := os.Stat(filepath.Join(root, "onboarding-ktp", "onb_1", "a.jpg")); err != nil {
		t.Fatalf("berkas harus ada di disk: %v", err)
	}
}

// Objek yang tidak ada bukan error: "tidak ada pembanding" adalah keadaan yang
// pemanggilnya sudah tangani, dan menjadikannya error menyembunyikannya di 500.
func TestStorage_MissingObjectIsNotAnError(t *testing.T) {
	s := New(t.TempDir())

	got, err := s.Download(context.Background(), "onboarding-ktp", "tidak/ada.jpg")
	if err != nil {
		t.Fatalf("tidak boleh error: %v", err)
	}
	if got != nil {
		t.Fatalf("harus nil, dapat %q", got)
	}
}

func TestStorage_DeleteIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()

	if _, err := s.Upload(ctx, "b", "k.jpg", []byte("x"), ""); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := s.Delete(ctx, "b", "k.jpg"); err != nil {
		t.Fatalf("delete pertama: %v", err)
	}
	// Menghapus yang sudah hilang bukan kegagalan — jalur pembersihan error
	// memanggilnya tanpa tahu apakah unggahannya sempat berhasil.
	if err := s.Delete(ctx, "b", "k.jpg"); err != nil {
		t.Fatalf("delete kedua harus idempoten: %v", err)
	}
}

// Session id masuk ke dalam key dan berasal dari request. Menyusun path dari
// nilai yang belum diperiksa adalah bagaimana `../../` keluar dari akar.
func TestStorage_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	ctx := context.Background()

	cases := map[string]struct{ bucket, key string }{
		"key naik satu tingkat": {"onboarding-ktp", "../escaped.jpg"},
		"key naik beberapa":     {"onboarding-ktp", "a/../../../escaped.jpg"},
		"bucket naik":           {"..", "x.jpg"},
		"bucket absolut":        {"/etc", "passwd"},
		"key absolut":           {"onboarding-ktp", "/etc/passwd"},
		"key titik":             {"onboarding-ktp", "."},
		"key kosong":            {"onboarding-ktp", ""},
		"bucket kosong":         {"", "x.jpg"},
		"null byte":             {"onboarding-ktp", "a\x00b.jpg"},
		"skema di dalam key":    {"onboarding-ktp", "local://b/k.jpg"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Upload(ctx, c.bucket, c.key, []byte("x"), ""); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("upload harus menolak, dapat %v", err)
			}
			if _, err := s.Download(ctx, c.bucket, c.key); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("download harus menolak, dapat %v", err)
			}
			if err := s.Delete(ctx, c.bucket, c.key); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("delete harus menolak, dapat %v", err)
			}
		})
	}

	// Dan tidak ada apa pun yang bocor ke luar akar.
	parent := filepath.Dir(root)
	if _, err := os.Stat(filepath.Join(parent, "escaped.jpg")); !os.IsNotExist(err) {
		t.Fatal("ada berkas yang tertulis di luar akar")
	}
}

// Isinya foto e-KTP terenkripsi; pengguna lain di host yang sama tidak punya
// alasan bisa membacanya.
func TestStorage_FileIsNotWorldReadable(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	if _, err := s.Upload(context.Background(), "b", "k.jpg", []byte("x"), ""); err != nil {
		t.Fatalf("upload: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, "b", "k.jpg"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mau 0600, dapat %o", perm)
	}
}

// Penulisan ulang key yang sama harus mengganti isinya, bukan menggabungkan.
func TestStorage_OverwriteReplacesContent(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()

	if _, err := s.Upload(ctx, "b", "k.jpg", []byte("panjang sekali isinya"), ""); err != nil {
		t.Fatalf("upload pertama: %v", err)
	}
	if _, err := s.Upload(ctx, "b", "k.jpg", []byte("pendek"), ""); err != nil {
		t.Fatalf("upload kedua: %v", err)
	}

	got, err := s.Download(ctx, "b", "k.jpg")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(got) != "pendek" {
		t.Fatalf("dapat %q", got)
	}
}

// Berkas sementara tidak boleh tertinggal: satu disk-full yang menumpuk
// pecahan .upload-* akan tidak terlihat sampai direktorinya penuh.
func TestStorage_NoTempFilesLeftBehind(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	if _, err := s.Upload(context.Background(), "b", "k.jpg", []byte("x"), ""); err != nil {
		t.Fatalf("upload: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "b"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("berkas sementara tertinggal: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("mau 1 berkas, dapat %d", len(entries))
	}
}

func TestParseObjectPath(t *testing.T) {
	cases := []struct {
		path   string
		bucket string
		key    string
		ok     bool
	}{
		{"local://b/k.jpg", "b", "k.jpg", true},
		{"local://b/sess/k.jpg", "b", "sess/k.jpg", true},
		{"s3://b/k.jpg", "", "", false}, // bukan skema milik localfs
		{"local://b", "", "", false},
		{"local://", "", "", false},
		{"", "", "", false},
	}

	for _, c := range cases {
		bucket, key, ok := ParseObjectPath(c.path)
		if ok != c.ok || bucket != c.bucket || key != c.key {
			t.Errorf("%q: dapat (%q, %q, %v), mau (%q, %q, %v)",
				c.path, bucket, key, ok, c.bucket, c.key, c.ok)
		}
	}
}
