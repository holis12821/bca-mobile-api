package websocket

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 30 * time.Second
	pingPeriod     = 25 * time.Second // must be < pongWait
	maxMessageSize = 64 * 1024        // 64 KB
)

// Client represents a single WebSocket connection.
type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	SessionID string
	QueueID   string
	Role      Role
	done      chan struct{}
}

// NewClient creates a new WebSocket client.
func NewClient(hub *Hub, conn *websocket.Conn, sessionID, queueID string, role Role) *Client {
	return &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan []byte, 64),
		SessionID: sessionID,
		QueueID:   queueID,
		Role:      role,
		done:      make(chan struct{}),
	}
}

// Send queues a message to be sent to the client.
func (c *Client) Send(data []byte) {
	select {
	case c.send <- data:
	default:
		slog.Warn("ws: send buffer full, dropping message",
			"session_id", c.SessionID,
			"role", c.Role,
		)
	}
}

// Close signals the client to shut down.
func (c *Client) Close() {
	select {
	case <-c.done:
		// already closed
	default:
		close(c.done)
	}
}

// Run starts the read and write pumps. Blocks until the connection closes.
func (c *Client) Run() {
	go c.writePump()
	c.readPump()
}

// readPump reads messages from the WebSocket connection.
func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("ws: read error",
					"session_id", c.SessionID,
					"role", c.Role,
					"error", err,
				)
			}
			return
		}

		var msg onboarding.SignalMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Warn("ws: invalid message format",
				"session_id", c.SessionID,
				"error", err,
			)
			continue
		}

		c.handleMessage(msg)
	}
}

// writePump writes messages from the send channel to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.done:
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}
	}
}

// handleMessage processes incoming signaling messages.
func (c *Client) handleMessage(msg onboarding.SignalMessage) {
	switch msg.Type {
	case "join":
		// Join is handled at connection time; no-op here
		return

	case "offer", "answer", "ice_candidate":
		// Relay WebRTC signaling to peer
		c.hub.RelayToPeer(c, msg)

	case "media_control":
		// Relay media control to peer
		c.hub.RelayToPeer(c, msg)

	case "instruction":
		// Agent sends instruction text to nasabah
		if c.Role == RoleAgent {
			c.hub.SendToSession(c.SessionID, RoleNasabah, msg)
		}

	default:
		slog.Warn("ws: unknown message type",
			"session_id", c.SessionID,
			"type", msg.Type,
		)
	}
}