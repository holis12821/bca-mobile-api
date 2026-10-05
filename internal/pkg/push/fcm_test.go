package push

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
)

// stubTokens stands in for DeviceRepo. It records the overrideMuted flag because
// that flag is the whole of the push_notification_enabled decision: SECURITY
// notifications must reach a handset whose owner muted everything else.
type stubTokens struct {
	tokens       []string
	err          error
	lastOverride bool
}

func (s *stubTokens) ListPushTokens(_ context.Context, _ uuid.UUID, overrideMuted bool) ([]string, error) {
	s.lastOverride = overrideMuted
	return s.tokens, s.err
}

type stubInvalidator struct {
	cleared []string
	err     error
}

func (s *stubInvalidator) ClearPushToken(_ context.Context, token string) error {
	if s.err != nil {
		return s.err
	}
	s.cleared = append(s.cleared, token)
	return nil
}

// sendResponse is one canned FCM reply, keyed by the device token in the body.
type sendResponse struct {
	status int
	body   string
}

// stubTransport answers both hops: minting the OAuth2 access token and sending a
// message. Intercepting at the RoundTripper means the pusher is exercised through
// its real HTTP path, including the signed assertion.
type stubTransport struct {
	tokenCalls int
	sendBodies []string
	perToken   map[string]sendResponse
	defaultRsp sendResponse
	netErr     error
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.netErr != nil {
		return nil, s.netErr
	}

	raw, _ := io.ReadAll(req.Body)

	if strings.Contains(req.URL.Path, "/token") {
		s.tokenCalls++
		// The assertion must be a three-part JWT, or the pusher is not actually
		// signing anything.
		if form := string(raw); !strings.Contains(form, "assertion=") {
			return nil, fmt.Errorf("token request carried no assertion: %s", form)
		}
		return jsonResponse(http.StatusOK, `{"access_token":"ya29.stub","expires_in":3600}`), nil
	}

	s.sendBodies = append(s.sendBodies, string(raw))

	var msg struct {
		Message struct {
			Token string `json:"token"`
		} `json:"message"`
	}
	_ = json.Unmarshal(raw, &msg)

	if rsp, ok := s.perToken[msg.Message.Token]; ok {
		return jsonResponse(rsp.status, rsp.body), nil
	}
	if s.defaultRsp.status != 0 {
		return jsonResponse(s.defaultRsp.status, s.defaultRsp.body), nil
	}
	return jsonResponse(http.StatusOK, `{"name":"projects/bca-test/messages/1"}`), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// newTestPusher builds a real FCMPusher from a real generated key, then swaps
// only its HTTP client. Going through NewFCMPusher is the point: the credential
// parsing and the boot-time validation are part of what is under test.
func newTestPusher(t *testing.T, tokens TokenStore, dead TokenInvalidator, tr *stubTransport) *FCMPusher {
	t.Helper()

	p, err := NewFCMPusher(tokens, dead, FCMConfig{
		CredentialsFile: writeCredentials(t),
		Timeout:         2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewFCMPusher: %v", err)
	}
	p.http = &http.Client{Transport: tr}
	return p
}

// writeCredentials emits a service account file with a genuine RSA key, so the
// PEM parsing and RS256 signing run for real.
func writeCredentials(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	account := map[string]string{
		"type":         "service_account",
		"project_id":   "bca-test",
		"client_email": "push@bca-test.iam.gserviceaccount.com",
		"private_key":  string(keyPEM),
		"token_uri":    "https://oauth2.test/token",
	}
	raw, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}

	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return path
}

func TestFCMPusher_RejectsBadCredentialsAtBoot(t *testing.T) {
	tokens := &stubTokens{}
	dead := &stubInvalidator{}

	cases := map[string]string{
		"not json":        `{`,
		"wrong type":      `{"type":"authorized_user","project_id":"p","client_email":"e","private_key":"k"}`,
		"no project":      `{"type":"service_account","client_email":"e","private_key":"k"}`,
		"unusable key":    `{"type":"service_account","project_id":"p","client_email":"e","private_key":"not a pem"}`,
		"blank client id": `{"type":"service_account","project_id":"p","client_email":"  ","private_key":"k"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFCMPusher(tokens, dead, FCMConfig{CredentialsFile: path}); err == nil {
				t.Fatal("kredensial tidak sah harus menggagalkan boot, bukan diterima")
			}
		})
	}

	// And a file that is not there at all.
	if _, err := NewFCMPusher(tokens, dead, FCMConfig{CredentialsFile: "/nope/missing.json"}); err == nil {
		t.Error("kredensial yang tidak ada harus menggagalkan boot")
	}
}

func TestFCMPusher_NoTokensNeverCallsFCM(t *testing.T) {
	tokens := &stubTokens{}
	dead := &stubInvalidator{}
	tr := &stubTransport{}
	p := newTestPusher(t, tokens, dead, tr)

	if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi",
		map[string]string{"type": notify.TypeTransaction}); err != nil {
		t.Fatalf("nol token bukan error: %v", err)
	}

	// Nol token juga tidak boleh menebus access token: nasabah yang belum pernah
	// membuka aplikasi tidak boleh menimbulkan satu pun panggilan ke Google.
	if tr.tokenCalls != 0 || len(tr.sendBodies) != 0 {
		t.Errorf("FCM dipanggil padahal tidak ada token: token=%d send=%d",
			tr.tokenCalls, len(tr.sendBodies))
	}
}

func TestFCMPusher_SendsToEveryDeviceAndForwardsData(t *testing.T) {
	tokens := &stubTokens{tokens: []string{"tok-a", "tok-b"}}
	dead := &stubInvalidator{}
	tr := &stubTransport{}
	p := newTestPusher(t, tokens, dead, tr)

	data := map[string]string{
		"type":      notify.TypeTransaction,
		"deep_link": "bcamobile://transaction/abc",
	}
	if err := p.Push(context.Background(), uuid.New(), "Transfer berhasil", "Rp 100.000", data); err != nil {
		t.Fatalf("push: %v", err)
	}

	if len(tr.sendBodies) != 2 {
		t.Fatalf("pesan terkirim: %d, harusnya 2", len(tr.sendBodies))
	}
	// Satu access token untuk kedua perangkat, bukan satu per perangkat.
	if tr.tokenCalls != 1 {
		t.Errorf("access token ditebus %d kali, harusnya 1", tr.tokenCalls)
	}
	// data diteruskan apa adanya — deep_link-nya yang dipakai client untuk
	// membuka layar yang benar.
	if !strings.Contains(tr.sendBodies[0], "bcamobile://transaction/abc") {
		t.Errorf("deep_link tidak ikut terkirim: %s", tr.sendBodies[0])
	}
	if !strings.Contains(tr.sendBodies[0], "Transfer berhasil") {
		t.Errorf("judul tidak ikut terkirim: %s", tr.sendBodies[0])
	}
	if len(dead.cleared) != 0 {
		t.Errorf("pengiriman sukses tidak boleh menghapus token: %v", dead.cleared)
	}
}

func TestFCMPusher_UnregisteredTokenIsClearedAndOthersStillDelivered(t *testing.T) {
	tokens := &stubTokens{tokens: []string{"tok-dead", "tok-live"}}
	dead := &stubInvalidator{}
	tr := &stubTransport{perToken: map[string]sendResponse{
		"tok-dead": {status: http.StatusNotFound, body: `{"error":{"code":404,"status":"NOT_FOUND",
			"message":"Requested entity was not found.","details":[
			{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`},
	}}
	p := newTestPusher(t, tokens, dead, tr)

	// Satu perangkat mati bukan kegagalan notifikasinya: ponsel di tangan
	// nasabah tetap menerimanya.
	if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi",
		map[string]string{"type": notify.TypeTransaction}); err != nil {
		t.Fatalf("token mati tidak boleh membuat push gagal: %v", err)
	}

	if len(tr.sendBodies) != 2 {
		t.Errorf("token pertama gagal menghentikan sisanya: %d pesan", len(tr.sendBodies))
	}
	if len(dead.cleared) != 1 || dead.cleared[0] != "tok-dead" {
		t.Errorf("token yang dihapus: %v, harusnya hanya tok-dead", dead.cleared)
	}
}

func TestFCMPusher_InvalidArgumentIsAlsoCleared(t *testing.T) {
	tokens := &stubTokens{tokens: []string{"tok-broken"}}
	dead := &stubInvalidator{}
	tr := &stubTransport{defaultRsp: sendResponse{
		status: http.StatusBadRequest,
		body: `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad token","details":[
			{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"}]}}`,
	}}
	p := newTestPusher(t, tokens, dead, tr)

	if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(dead.cleared) != 1 {
		t.Errorf("token cacat harus dihapus: %v", dead.cleared)
	}
}

// FCM yang sedang bermasalah TIDAK boleh menghapus token. Kalau ia menghapus,
// pemadaman lima menit berubah jadi nasabah yang berhenti menerima push sampai
// aplikasinya dibuka lagi.
func TestFCMPusher_TransientFailureKeepsToken(t *testing.T) {
	for name, rsp := range map[string]sendResponse{
		"unavailable": {status: http.StatusServiceUnavailable,
			body: `{"error":{"code":503,"status":"UNAVAILABLE","message":"backend down","details":[
				{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNAVAILABLE"}]}}`},
		"internal": {status: http.StatusInternalServerError,
			body: `{"error":{"code":500,"status":"INTERNAL","message":"oops"}}`},
		"quota": {status: http.StatusTooManyRequests,
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`},
		"our own auth is wrong": {status: http.StatusUnauthorized,
			body: `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"bad credentials"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			tokens := &stubTokens{tokens: []string{"tok-live"}}
			dead := &stubInvalidator{}
			p := newTestPusher(t, tokens, dead, &stubTransport{defaultRsp: rsp})

			err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", nil)
			if err == nil {
				t.Error("kegagalan sementara harus dilaporkan sebagai error")
			}
			if len(dead.cleared) != 0 {
				t.Errorf("token tidak boleh dihapus karena FCM bermasalah: %v", dead.cleared)
			}
		})
	}
}

// Sakelar push_notification_enabled diredam di lapisan token, dan SECURITY
// menembusnya — keputusan produk yang dicatat di SKILL.md §3.2.
func TestFCMPusher_SecurityOverridesMuteSwitch(t *testing.T) {
	cases := map[string]struct {
		notifType string
		want      bool
	}{
		"security menembus": {notify.TypeSecurity, true},
		"transaksi tidak":   {notify.TypeTransaction, false},
		"promo tidak":       {notify.TypePromo, false},
		"tanpa tipe":        {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tokens := &stubTokens{tokens: []string{"tok-live"}}
			p := newTestPusher(t, tokens, &stubInvalidator{}, &stubTransport{})

			data := map[string]string{}
			if tc.notifType != "" {
				data["type"] = tc.notifType
			}
			if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", data); err != nil {
				t.Fatalf("push: %v", err)
			}
			if tokens.lastOverride != tc.want {
				t.Errorf("overrideMuted = %v, want %v", tokens.lastOverride, tc.want)
			}
		})
	}
}

// Access token dipakai ulang antar notifikasi. Menebus satu token per notifikasi
// menambah satu perjalanan jaringan ke jalur yang uangnya sudah commit.
func TestFCMPusher_ReusesAccessTokenUntilItNearlyExpires(t *testing.T) {
	tokens := &stubTokens{tokens: []string{"tok-live"}}
	tr := &stubTransport{}
	p := newTestPusher(t, tokens, &stubInvalidator{}, tr)

	base := time.Now()
	p.nowFunc = func() time.Time { return base }

	for i := range 3 {
		if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", nil); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	if tr.tokenCalls != 1 {
		t.Errorf("access token ditebus %d kali untuk 3 notifikasi, harusnya 1", tr.tokenCalls)
	}

	// Lewat masa berlakunya (dikurangi skew) → ditebus ulang.
	p.nowFunc = func() time.Time { return base.Add(time.Hour) }
	if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", nil); err != nil {
		t.Fatalf("push setelah token kedaluwarsa: %v", err)
	}
	if tr.tokenCalls != 2 {
		t.Errorf("token kedaluwarsa tidak ditebus ulang: %d panggilan", tr.tokenCalls)
	}
}

func TestFCMPusher_TokenStoreFailureIsReported(t *testing.T) {
	tokens := &stubTokens{err: fmt.Errorf("database gone")}
	tr := &stubTransport{}
	p := newTestPusher(t, tokens, &stubInvalidator{}, tr)

	if err := p.Push(context.Background(), uuid.New(), "Judul", "Isi", nil); err == nil {
		t.Error("kegagalan baca token harus dilaporkan")
	}
	if tr.tokenCalls != 0 {
		t.Error("tidak boleh memanggil FCM ketika daftar token tidak terbaca")
	}
}
