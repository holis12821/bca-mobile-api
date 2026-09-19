package handler

import (
	"log/slog"
	"net/http"

	gorillaWS "github.com/gorilla/websocket"

	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	ws "github.com/holis12821/bca-mobile-api/internal/websocket"
)

var upgrader = gorillaWS.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // TODO: restrict in production
	},
}

// SignalingHandler handles WebSocket connections for video call signaling.
type SignalingHandler struct {
	hub *ws.Hub
	jwt *crypto.JWTManager
}

func NewSignalingHandler(hub *ws.Hub, jwt *crypto.JWTManager) *SignalingHandler {
	return &SignalingHandler{hub: hub, jwt: jwt}
}

// HandleSignaling upgrades to WebSocket and runs the signaling client.
// GET /v1/onboarding/video-call/signal?token=<jwt>&role=nasabah
func (h *SignalingHandler) HandleSignaling(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	roleStr := r.URL.Query().Get("role")

	if tokenStr == "" || roleStr == "" {
		http.Error(w, "missing token or role", http.StatusBadRequest)
		return
	}

	// Validate role
	var role ws.Role
	switch roleStr {
	case "nasabah":
		role = ws.RoleNasabah
	case "agent":
		role = ws.RoleAgent
	default:
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}

	// Verify JWT
	var sessionID, queueID string
	if h.jwt != nil {
		claims, err := h.jwt.VerifyToken(tokenStr, crypto.TokenTypeSignaling)
		if err != nil {
			slog.Warn("ws: invalid signaling token", "error", err)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		sessionID = claims.Subject
		queueID = claims.SessionID // we stored queueID in SessionID field
	} else {
		// Dev mode: parse from query
		sessionID = r.URL.Query().Get("session_id")
		queueID = r.URL.Query().Get("queue_id")
		if sessionID == "" {
			http.Error(w, "missing session_id", http.StatusBadRequest)
			return
		}
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws: upgrade failed", "error", err)
		return
	}

	client := ws.NewClient(h.hub, conn, sessionID, queueID, role)
	h.hub.Register(client)
	client.Run() // blocks until disconnect
}