package handler

import (
	"log/slog"
	"net/http"
	"strings"

	gorillaWS "github.com/gorilla/websocket"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	ws "github.com/holis12821/bca-mobile-api/internal/websocket"
)

// SignalingHandler handles WebSocket connections for video call signaling.
type SignalingHandler struct {
	hub      *ws.Hub
	jwt      *crypto.JWTManager
	upgrader gorillaWS.Upgrader
}

// NewSignalingHandler builds the handler. allowedOrigins is the same list the
// CORS middleware uses: a WebSocket upgrade bypasses CORS entirely, so without
// an Origin check any web page could open a socket against this endpoint with
// a token it phished (cross-site WebSocket hijacking). Native apps send no
// Origin header at all and are unaffected.
func NewSignalingHandler(hub *ws.Hub, jwt *crypto.JWTManager, allowedOrigins []string) *SignalingHandler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.ToLower(strings.TrimSpace(o))] = true
	}

	return &SignalingHandler{
		hub: hub,
		jwt: jwt,
		upgrader: gorillaWS.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				origin := strings.ToLower(strings.TrimSpace(r.Header.Get("Origin")))
				if origin == "" {
					return true // native client, no browser origin to check
				}
				if allowed["*"] {
					return true
				}
				return allowed[origin]
			},
		},
	}
}

// HandleSignaling upgrades to WebSocket and runs the signaling client.
// GET /v1/onboarding/video-call/signal?token=<jwt>
//
// The role comes from the signed token, never from the query string: a nasabah
// holding their own token must not be able to attach as the agent side of the
// call and receive what the agent receives.
func (h *SignalingHandler) HandleSignaling(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	if h.jwt == nil {
		// Without a JWT manager there is no way to establish who is calling.
		slog.Error("ws: signaling requested but no JWT manager is configured")
		http.Error(w, "signaling unavailable", http.StatusServiceUnavailable)
		return
	}

	claims, err := h.jwt.VerifyToken(tokenStr, crypto.TokenTypeSignaling)
	if err != nil {
		slog.Warn("ws: invalid signaling token", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	sessionID := claims.Subject
	queueID := claims.SessionID // the signaling token stores queue_id here
	if sessionID == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	if !onboarding.ValidSignalingRole(claims.Role) {
		slog.Warn("ws: signaling token carries no usable role",
			"session_id", sessionID,
			"role", claims.Role,
		)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	role := ws.Role(claims.Role)

	// Upgrade to WebSocket
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws: upgrade failed", "error", err)
		return
	}

	client := ws.NewClient(h.hub, conn, sessionID, queueID, role)
	h.hub.Register(client)
	client.Run() // blocks until disconnect
}
