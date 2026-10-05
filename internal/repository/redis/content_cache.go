package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/content"
)

// ContentCache menyimpan dua halaman statis: Pusat Bantuan dan Kontak CS.
//
// Tinggal di instance cache, bukan session. Isinya sama untuk semua orang dan
// berubah beberapa kali setahun, jadi ini justru kasus langka di mana TTL
// panjang benar.
type ContentCache struct {
	client *goredis.Client
}

func NewContentCache(client *goredis.Client) *ContentCache {
	return &ContentCache{client: client}
}

const (
	// Versi ikut di dalam kunci. Mengubah bentuk response berarti menaikkan
	// angkanya, dan entri lama kedaluwarsa sendiri — tidak ada penghapusan
	// manual yang bisa terlupakan saat deploy.
	helpCenterKey = "content:help_center:v1"
	contactCSKey  = "content:contact_cs:v1"

	// 24 jam: konten ini diubah lewat SQL, dan siapa pun yang mengubahnya tidak
	// akan menjalankan invalidasi cache. TTL sehari adalah janji bahwa
	// perubahan pasti terlihat tanpa ada yang perlu diingat.
	contentTTL = 24 * time.Hour
)

// GetHelpCenter mengembalikan FAQ dari cache. nil, nil bila tidak ada.
func (c *ContentCache) GetHelpCenter(ctx context.Context) (*content.HelpCenterResponse, error) {
	data, err := c.client.Get(ctx, helpCenterKey).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get help center cache: %w", err)
	}

	var resp content.HelpCenterResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		// Entri rusak diperlakukan sebagai cache miss: database masih punya
		// jawabannya, dan mematikan halaman bantuan karena satu entri busuk
		// justru menutup pintu keluar nasabah.
		return nil, nil
	}
	return &resp, nil
}

func (c *ContentCache) SetHelpCenter(ctx context.Context, resp *content.HelpCenterResponse) error {
	if resp == nil {
		return nil
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal help center: %w", err)
	}
	if err := c.client.Set(ctx, helpCenterKey, data, contentTTL).Err(); err != nil {
		return fmt.Errorf("set help center cache: %w", err)
	}
	return nil
}

// GetContactCS mengembalikan kontak CS dari cache. nil, nil bila tidak ada.
func (c *ContentCache) GetContactCS(ctx context.Context) (*content.ContactCS, error) {
	data, err := c.client.Get(ctx, contactCSKey).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get contact cs cache: %w", err)
	}

	var contact content.ContactCS
	if err := json.Unmarshal(data, &contact); err != nil {
		return nil, nil
	}
	return &contact, nil
}

func (c *ContentCache) SetContactCS(ctx context.Context, contact *content.ContactCS) error {
	if contact == nil {
		return nil
	}
	data, err := json.Marshal(contact)
	if err != nil {
		return fmt.Errorf("marshal contact cs: %w", err)
	}
	if err := c.client.Set(ctx, contactCSKey, data, contentTTL).Err(); err != nil {
		return fmt.Errorf("set contact cs cache: %w", err)
	}
	return nil
}
