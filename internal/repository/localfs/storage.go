// Package localfs menyimpan objek onboarding di berkas lokal.
//
// Dibuat karena satu-satunya implementasi ObjectStorage yang aktif di luar
// produksi adalah MockObjectStorage, yang **tidak menulis apa pun** dan hanya
// mengembalikan path `s3://` palsu. Akibatnya dua hal yang terlihat seperti bug
// terpisah padahal berakar di sini:
//
//  1. Foto e-KTP "tidak terlampir": kolom photo_path di database menunjuk objek
//     yang tidak pernah ada.
//  2. Face match biometrik tidak punya pembanding: Download mengembalikan
//     nil, nil, jadi verifikasi wajah menolak dengan no_enrolled_reference
//     bahkan dengan mesin ML yang sungguhan.
//
// Ini bukan pengganti object storage produksi. Yang dituju adalah pengembangan
// dan portofolio: tanpa kredensial, tanpa layanan luar, tapi objeknya benar-benar
// ada dan benar-benar bisa dibaca kembali.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// safeSegment menerima hanya yang dipakai sebagai nama bucket dan komponen key.
//
// Isi key dibentuk kode (session id + UUID), tapi session id berasal dari
// request. Menyusun path dari nilai yang belum diperiksa adalah bagaimana
// `../../` keluar dari direktori penyimpanan, jadi setiap segmen disaring —
// bukan hanya dipercaya karena "pemanggilnya internal".
//
// Titik termasuk yang diizinkan karena nama berkas punya ekstensi. Konsekuensinya
// pola ini **cocok** dengan "." dan ".." — ditemukan oleh test traversal berkas
// ini sendiri — jadi keduanya ditolak terpisah di isSafeSegment. Mengandalkan
// pemeriksaan prefiks di akhir resolve saja tidak cukup: "bucket/.." saling
// menghapus jadi akar, tetap lolos prefiks, dan menulis di luar bucket yang
// dituju.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// isSafeSegment menolak segmen yang tidak boleh jadi komponen path.
func isSafeSegment(seg string) bool {
	if seg == "." || seg == ".." {
		return false
	}
	return safeSegment.MatchString(seg)
}

// ErrUnsafePath dikembalikan untuk bucket atau key yang tidak bisa dipetakan ke
// berkas dengan aman.
var ErrUnsafePath = errors.New("localfs: bucket atau key tidak aman")

// Storage adalah ObjectStorage di atas satu direktori akar.
type Storage struct {
	root string
}

// New membuat Storage dengan direktori akar yang diberikan.
//
// Direktorinya dibuat saat objek pertama ditulis, bukan di sini: kegagalan
// mkdir saat boot akan menghentikan server untuk sesuatu yang mungkin tidak
// pernah dipakai di proses ini.
func New(root string) *Storage {
	return &Storage{root: root}
}

// Upload menulis data ke <root>/<bucket>/<key> dan mengembalikan path objeknya.
//
// Penulisannya atomik: ke berkas sementara di direktori yang sama lalu rename.
// Tanpa itu, proses yang mati di tengah penulisan meninggalkan berkas terpotong
// yang tetap lolos pemeriksaan "objeknya ada" — dan foto KTP yang terpotong
// gagal di tempat yang jauh dari sebabnya.
func (s *Storage) Upload(
	_ context.Context, bucket, key string, data []byte, _ string,
) (string, error) {
	target, err := s.resolve(bucket, key)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("localfs mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return "", fmt.Errorf("localfs temp: %w", err)
	}
	tmpName := tmp.Name()

	// Dibersihkan pada setiap jalur gagal. Tanpa ini satu disk-full meninggalkan
	// pecahan .upload-* yang menumpuk sampai tidak ada yang menyadarinya.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("localfs write: %w", err)
	}
	// Sync sebelum rename: rename yang terlihat sukses tidak menjamin isinya
	// sudah sampai di disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("localfs sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("localfs close: %w", err)
	}
	// 0600: isinya foto e-KTP terenkripsi. Pengguna lain di host yang sama tidak
	// punya alasan bisa membacanya.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", fmt.Errorf("localfs chmod: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("localfs rename: %w", err)
	}
	tmpName = ""

	return objectPath(bucket, key), nil
}

// Download membaca objek kembali.
//
// Objek yang tidak ada mengembalikan nil, nil — bukan error. Itu keadaan yang
// pemanggilnya sudah harus tangani ("tidak ada pembanding"), dan menjadikannya
// error akan menyembunyikannya di balik 500.
func (s *Storage) Download(_ context.Context, bucket, key string) ([]byte, error) {
	target, err := s.resolve(bucket, key)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localfs read: %w", err)
	}
	return data, nil
}

// Delete menghapus objek. Objek yang sudah tidak ada bukan kegagalan.
func (s *Storage) Delete(_ context.Context, bucket, key string) error {
	target, err := s.resolve(bucket, key)
	if err != nil {
		return err
	}

	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("localfs remove: %w", err)
	}
	return nil
}

// resolve memetakan bucket+key ke path berkas, menolak apa pun yang bisa keluar
// dari direktori akar.
func (s *Storage) resolve(bucket, key string) (string, error) {
	if !isSafeSegment(bucket) {
		return "", fmt.Errorf("%w: bucket %q", ErrUnsafePath, bucket)
	}

	// Key boleh memuat "/" sebagai pemisah direktori; setiap segmennya disaring
	// sendiri lewat isSafeSegment, yang itulah yang menolak "." dan "..".
	segments := strings.Split(key, "/")
	if len(segments) == 0 {
		return "", fmt.Errorf("%w: key kosong", ErrUnsafePath)
	}
	for _, seg := range segments {
		if !isSafeSegment(seg) {
			return "", fmt.Errorf("%w: segmen key %q", ErrUnsafePath, seg)
		}
	}

	target := filepath.Join(append([]string{s.root, bucket}, segments...)...)

	// Jaring terakhir: apa pun yang lolos saringan di atas tapi tetap jatuh di
	// luar akar ditolak. Murah, dan satu-satunya yang menangkap kasus yang tidak
	// terpikirkan.
	rootAbs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("localfs abs root: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("localfs abs target: %w", err)
	}
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q di luar akar", ErrUnsafePath, key)
	}

	return targetAbs, nil
}

// objectPath adalah bentuk yang disimpan di kolom photo_path.
//
// Skema `local://` dipakai supaya baris lama yang menyimpan `s3://...` palsu
// bisa dibedakan dari objek yang benar-benar ada.
func objectPath(bucket, key string) string {
	return "local://" + bucket + "/" + key
}

// ParseObjectPath memecah path `local://bucket/key` kembali jadi komponennya.
//
// Dipakai pada jalur yang hanya menyimpan satu string path lalu perlu membaca
// objeknya lagi — misalnya face match yang mengambil foto KTP sebagai pembanding.
func ParseObjectPath(path string) (bucket, key string, ok bool) {
	trimmed, found := strings.CutPrefix(path, "local://")
	if !found {
		return "", "", false
	}
	bucket, key, found = strings.Cut(trimmed, "/")
	if !found || bucket == "" || key == "" {
		return "", "", false
	}
	return bucket, key, true
}
