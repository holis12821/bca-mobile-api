package websocket

import (
	"encoding/json"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// newTestClient builds a client with no real connection. Everything the hub
// does — routing, registration, room cleanup — goes through the send channel,
// so a nil *websocket.Conn is enough as long as the pumps are never started.
func newTestClient(hub *Hub, sessionID string, role Role) *Client {
	return NewClient(hub, nil, sessionID, "q_test", role)
}

// recv reads one queued message, or reports that none arrived.
func recv(t *testing.T, c *Client) (onboarding.SignalMessage, bool) {
	t.Helper()
	select {
	case data := <-c.send:
		var msg onboarding.SignalMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("queued message is not a SignalMessage: %v", err)
		}
		return msg, true
	default:
		return onboarding.SignalMessage{}, false
	}
}

func TestHub_RelayReachesThePeerOnly(t *testing.T) {
	hub := NewHub()
	nasabah := newTestClient(hub, "onb_1", RoleNasabah)
	agent := newTestClient(hub, "onb_1", RoleAgent)
	hub.Register(nasabah)
	hub.Register(agent)

	hub.RelayToPeer(nasabah, onboarding.SignalMessage{Type: "offer", SDP: "v=0"})

	got, ok := recv(t, agent)
	if !ok {
		t.Fatal("agent should have received the offer")
	}
	if got.Type != "offer" || got.SDP != "v=0" {
		t.Errorf("unexpected relayed message: %+v", got)
	}
	if _, echoed := recv(t, nasabah); echoed {
		t.Error("the sender should not receive its own message")
	}
}

func TestHub_RoomsAreIsolatedBySession(t *testing.T) {
	hub := NewHub()
	nasabahA := newTestClient(hub, "onb_a", RoleNasabah)
	agentB := newTestClient(hub, "onb_b", RoleAgent)
	hub.Register(nasabahA)
	hub.Register(agentB)

	hub.RelayToPeer(nasabahA, onboarding.SignalMessage{Type: "offer"})

	if _, leaked := recv(t, agentB); leaked {
		t.Error("a message must not cross into another session's room")
	}
}

func TestHub_RelayWithNoPeerIsDropped(t *testing.T) {
	hub := NewHub()
	nasabah := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(nasabah)

	// No agent in the room yet — this must not panic or block.
	hub.RelayToPeer(nasabah, onboarding.SignalMessage{Type: "ice_candidate"})
}

func TestHub_ReconnectReplacesTheOldConnection(t *testing.T) {
	hub := NewHub()
	first := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(first)

	second := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(second)

	// The replaced client is told to shut down...
	select {
	case <-first.done:
	default:
		t.Fatal("the replaced client should have been closed")
	}

	// ...and the room now points at the new one.
	agent := newTestClient(hub, "onb_1", RoleAgent)
	hub.Register(agent)
	hub.RelayToPeer(agent, onboarding.SignalMessage{Type: "instruction", Text: "Tunjukkan KTP"})

	if _, ok := recv(t, second); !ok {
		t.Error("the current connection should receive relayed messages")
	}
}

// Unregistering a connection that has already been replaced must not wipe the
// live one out of the room.
func TestHub_UnregisterStaleClientKeepsRoom(t *testing.T) {
	hub := NewHub()
	first := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(first)
	second := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(second)

	hub.Unregister(first)

	hub.SendToSession("onb_1", RoleNasabah, onboarding.SignalMessage{Type: "queue_update"})
	if _, ok := recv(t, second); !ok {
		t.Error("the live connection should still be registered")
	}
}

func TestHub_UnregisterLastClientDropsRoom(t *testing.T) {
	hub := NewHub()
	nasabah := newTestClient(hub, "onb_1", RoleNasabah)
	hub.Register(nasabah)
	hub.Unregister(nasabah)

	hub.mu.RLock()
	_, stillThere := hub.rooms["onb_1"]
	hub.mu.RUnlock()

	if stillThere {
		t.Error("an empty room should be cleaned up")
	}
}

func TestHub_BroadcastReachesBothSides(t *testing.T) {
	hub := NewHub()
	nasabah := newTestClient(hub, "onb_1", RoleNasabah)
	agent := newTestClient(hub, "onb_1", RoleAgent)
	hub.Register(nasabah)
	hub.Register(agent)

	hub.BroadcastToRoom("onb_1", onboarding.SignalMessage{Type: "call_ended"})

	if _, ok := recv(t, nasabah); !ok {
		t.Error("nasabah should receive the broadcast")
	}
	if _, ok := recv(t, agent); !ok {
		t.Error("agent should receive the broadcast")
	}
}

// Close is reachable from the hub and from the read pump at the same time.
func TestClient_CloseIsIdempotent(t *testing.T) {
	hub := NewHub()
	c := newTestClient(hub, "onb_1", RoleNasabah)

	c.Close()
	c.Close() // must not panic on a second close of the same channel
}

// A slow consumer must not block the hub: the send buffer fills and further
// messages are dropped rather than stalling the caller.
func TestClient_SendDropsWhenBufferIsFull(t *testing.T) {
	hub := NewHub()
	c := newTestClient(hub, "onb_1", RoleNasabah)

	for i := 0; i < cap(c.send)+10; i++ {
		c.Send([]byte(`{"type":"ice_candidate"}`))
	}

	if len(c.send) != cap(c.send) {
		t.Errorf("expected the buffer to be full at %d, got %d", cap(c.send), len(c.send))
	}
}
