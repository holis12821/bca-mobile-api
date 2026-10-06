package websocket

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// Role identifies which side of the call a connection belongs to.
type Role string

const (
	RoleNasabah Role = "nasabah"
	RoleAgent   Role = "agent"
)

// Room holds the two WebSocket connections for a video call session.
type Room struct {
	SessionID string
	QueueID   string
	Nasabah   *Client
	Agent     *Client
}

// Hub manages all active WebSocket rooms, keyed by session_id.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]*Room // session_id → Room
}

// NewHub creates a new signaling hub.
func NewHub() *Hub {
	return &Hub{
		rooms: make(map[string]*Room),
	}
}

// Register adds a client to the appropriate room.
func (h *Hub) Register(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.rooms[client.SessionID]
	if !ok {
		room = &Room{
			SessionID: client.SessionID,
			QueueID:   client.QueueID,
		}
		h.rooms[client.SessionID] = room
	}

	switch client.Role {
	case RoleNasabah:
		if room.Nasabah != nil {
			// Close old connection (reconnect scenario)
			room.Nasabah.Close()
		}
		room.Nasabah = client
	case RoleAgent:
		if room.Agent != nil {
			room.Agent.Close()
		}
		room.Agent = client
	}

	slog.Info("ws: client registered",
		"session_id", client.SessionID,
		"role", client.Role,
	)
}

// Unregister removes a client from its room.
func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.rooms[client.SessionID]
	if !ok {
		return
	}

	switch client.Role {
	case RoleNasabah:
		if room.Nasabah == client {
			room.Nasabah = nil
		}
	case RoleAgent:
		if room.Agent == client {
			room.Agent = nil
		}
	}

	// Clean up empty rooms
	if room.Nasabah == nil && room.Agent == nil {
		delete(h.rooms, client.SessionID)
	}

	slog.Info("ws: client unregistered",
		"session_id", client.SessionID,
		"role", client.Role,
	)
}

// RelayToPeer sends a message to the other side of the room.
func (h *Hub) RelayToPeer(sender *Client, msg onboarding.SignalMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.rooms[sender.SessionID]
	if !ok {
		return
	}

	var target *Client
	switch sender.Role {
	case RoleNasabah:
		target = room.Agent
	case RoleAgent:
		target = room.Nasabah
	}

	if target == nil {
		slog.Debug("ws: no peer to relay to",
			"session_id", sender.SessionID,
			"sender_role", sender.Role,
		)
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("ws: marshal relay message", "error", err)
		return
	}
	target.Send(data)
}

// SendToSession sends a message to a specific role in a session.
func (h *Hub) SendToSession(sessionID string, role Role, msg onboarding.SignalMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.rooms[sessionID]
	if !ok {
		return
	}

	var target *Client
	switch role {
	case RoleNasabah:
		target = room.Nasabah
	case RoleAgent:
		target = room.Agent
	}

	if target == nil {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	target.Send(data)
}

// SendToNasabah mengirim pesan ke sisi nasabah sebuah sesi.
//
// Ada supaya lapisan domain bisa mengirim tanpa mengenal tipe [Role]: paket ini sudah
// meng-import `onboarding`, jadi `onboarding` tidak boleh meng-import paket ini. Yang
// dipakai di sana adalah antarmuka yang dipenuhi metode ini secara struktural.
func (h *Hub) SendToNasabah(sessionID string, msg onboarding.SignalMessage) {
	h.SendToSession(sessionID, RoleNasabah, msg)
}

// SendToAgent mengirim pesan ke sisi petugas sebuah sesi.
//
// Ada karena `call_ended` dulu hanya dikirim ke nasabah: petugas yang nasabahnya menutup
// panggilan lebih dulu tidak melihat apa pun selain socket yang tiba-tiba sunyi, dan
// aplikasi desktop tidak punya cara membedakan panggilan yang selesai dari jaringan yang
// putus. Dua keadaan itu menuntut tindakan yang berbeda dari petugas.
//
// Berpasangan dengan [Hub.SendToNasabah] dan dipenuhi secara struktural oleh antarmuka
// di lapisan domain, dengan alasan yang sama: `onboarding` tidak boleh meng-import paket
// ini.
func (h *Hub) SendToAgent(sessionID string, msg onboarding.SignalMessage) {
	h.SendToSession(sessionID, RoleAgent, msg)
}

// HasRole melaporkan apakah sisi [role] sebuah sesi sedang tersambung.
//
// Dipakai untuk memutuskan apakah `agent_assigned` perlu ditahan: nasabah yang belum
// membuka socket tidak akan menerimanya, dan pesan yang menghilang tanpa jejak lebih buruk
// daripada yang tidak pernah dikirim.
func (h *Hub) HasRole(sessionID string, role Role) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.rooms[sessionID]
	if !ok {
		return false
	}
	switch role {
	case RoleNasabah:
		return room.Nasabah != nil
	case RoleAgent:
		return room.Agent != nil
	}
	return false
}

// BroadcastToRoom sends a message to all participants in a room.
func (h *Hub) BroadcastToRoom(sessionID string, msg onboarding.SignalMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.rooms[sessionID]
	if !ok {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	if room.Nasabah != nil {
		room.Nasabah.Send(data)
	}
	if room.Agent != nil {
		room.Agent.Send(data)
	}
}
