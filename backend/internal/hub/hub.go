package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/logger"
)

// HubMessage represents a SignalR-compatible message
type HubMessage struct {
	Type      string                 `json:"type"`       // invocation, stream_item, completion, ping, close
	Target    string                 `json:"target,omitempty"`  // Method name for invocations
	Arguments []interface{}          `json:"arguments,omitempty"`
	InvocationID string              `json:"invocation_id,omitempty"`
	Item      interface{}            `json:"item,omitempty"`     // For stream items
	Error     string                 `json:"error,omitempty"`    // For completion errors
	Headers   map[string]string      `json:"headers,omitempty"`
}

// Client represents a connected WebSocket client
type Client struct {
	ID            string
	UserID        string
	InstituteID   string
	Roles         []string
	Connection    *websocket.Conn
	Send          chan *HubMessage
	Groups        map[string]bool
	LastPing      time.Time
	ConnectedAt   time.Time
	Metadata      map[string]interface{}
	mu            sync.RWMutex
}

// Hub manages WebSocket connections and message routing
type Hub struct {
	config       *config.WebSocketConfig
	logger       *logger.Logger
	clients      map[string]*Client
	userClients  map[string]map[string]*Client // userID -> clientID -> Client
	groupClients map[string]map[string]*Client // groupName -> clientID -> Client
	mu           sync.RWMutex
	
	// Channels
	register   chan *Client
	unregister chan *Client
	broadcast  chan *BroadcastMessage
	
	// Rate limiting
	rateLimiter *RateLimiter
	
	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// BroadcastMessage represents a message to broadcast
type BroadcastMessage struct {
	Message    *HubMessage
	Filter     ClientFilter
	ExcludeIDs []string
}

// ClientFilter defines which clients receive a broadcast
type ClientFilter struct {
	UserIDs      []string
	InstituteIDs []string
	Roles        []string
	GroupNames   []string
	CustomFunc   func(*Client) bool
}

// RateLimiter implements token bucket rate limiting per connection
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*TokenBucket
	rate     int           // requests per second
	burst    int           // bucket size
	cleanup  time.Duration
}

// TokenBucket implements token bucket algorithm
type TokenBucket struct {
	tokens     float64
	lastRefill time.Time
	rate       float64
	burst      int
	mu         sync.Mutex
}

// NewHub creates a new WebSocket hub
func NewHub(cfg *config.WebSocketConfig, log *logger.Logger) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	
	h := &Hub{
		config:       cfg,
		logger:       log.WithComponent("websocket-hub"),
		clients:      make(map[string]*Client),
		userClients:  make(map[string]map[string]*Client),
		groupClients: make(map[string]map[string]*Client),
		register:     make(chan *Client, 100),
		unregister:   make(chan *Client, 100),
		broadcast:    make(chan *BroadcastMessage, 1000),
		rateLimiter:  NewRateLimiter(cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst),
		ctx:          ctx,
		cancel:       cancel,
	}
	
	return h
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(rate, burst int) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*TokenBucket),
		rate:    rate,
		burst:   burst,
		cleanup: 5 * time.Minute,
	}
	
	// Start cleanup goroutine
	go rl.cleanupLoop()
	
	return rl
}

// Allow checks if a request is allowed under rate limit
func (rl *RateLimiter) Allow(clientID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	bucket, exists := rl.buckets[clientID]
	if !exists {
		bucket = &TokenBucket{
			tokens:     float64(rl.burst),
			lastRefill: time.Now(),
			rate:       float64(rl.rate),
			burst:      rl.burst,
		}
		rl.buckets[clientID] = bucket
	}
	
	return bucket.Take()
}

// Take attempts to take a token from the bucket
func (tb *TokenBucket) Take() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens = min(float64(tb.burst), tb.tokens+elapsed*tb.rate)
	tb.lastRefill = now
	
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// cleanupLoop periodically removes stale buckets
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for id, bucket := range rl.buckets {
				bucket.mu.Lock()
				if now.Sub(bucket.lastRefill) > 10*time.Minute {
					delete(rl.buckets, id)
				}
				bucket.mu.Unlock()
			}
			rl.mu.Unlock()
		}
	}
}

// Start starts the hub
func (h *Hub) Start() {
	h.wg.Add(1)
	go h.run()
	h.logger.Info().Msg("WebSocket hub started")
}

// Stop stops the hub gracefully
func (h *Hub) Stop() {
	h.cancel()
	h.wg.Wait()
	h.logger.Info().Msg("WebSocket hub stopped")
}

// run is the main hub loop
func (h *Hub) run() {
	defer h.wg.Done()
	
	pingTicker := time.NewTicker(h.config.PingPeriod)
	defer pingTicker.Stop()
	
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)
			
		case client := <-h.unregister:
			h.unregisterClient(client)
			
		case msg := <-h.broadcast:
			h.broadcastMessage(msg)
			
		case <-pingTicker.C:
			h.pingClients()
			
		case <-h.ctx.Done():
			h.closeAllConnections()
			return
		}
	}
}

// registerClient registers a new client
func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	h.clients[client.ID] = client
	
	if _, exists := h.userClients[client.UserID]; !exists {
		h.userClients[client.UserID] = make(map[string]*Client)
	}
	h.userClients[client.UserID][client.ID] = client
	
	h.logger.Info().
		Str("client_id", client.ID).
		Str("user_id", client.UserID).
		Str("institute_id", client.InstituteID).
		Int("total_clients", len(h.clients)).
		Msg("Client connected")
}

// unregisterClient removes a client
func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	if _, exists := h.clients[client.ID]; !exists {
		return
	}
	
	delete(h.clients, client.ID)
	
	if userClients, ok := h.userClients[client.UserID]; ok {
		delete(userClients, client.ID)
		if len(userClients) == 0 {
			delete(h.userClients, client.UserID)
		}
	}
	
	// Remove from all groups
	for groupName := range client.Groups {
		h.removeFromGroupUnsafe(groupName, client.ID)
	}
	
	close(client.Send)
	
	h.logger.Info().
		Str("client_id", client.ID).
		Str("user_id", client.UserID).
		Int("total_clients", len(h.clients)).
		Msg("Client disconnected")
}

// removeFromGroupUnsafe removes client from group (must hold lock)
func (h *Hub) removeFromGroupUnsafe(groupName, clientID string) {
	if groupClients, ok := h.groupClients[groupName]; ok {
		delete(groupClients, clientID)
		if len(groupClients) == 0 {
			delete(h.groupClients, groupName)
		}
	}
}

// broadcastMessage sends a message to filtered clients
func (h *Hub) broadcastMessage(msg *BroadcastMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	var targets []*Client
	
	if msg.Filter.CustomFunc != nil {
		for _, client := range h.clients {
			if msg.Filter.CustomFunc(client) && !contains(msg.ExcludeIDs, client.ID) {
				targets = append(targets, client)
			}
		}
	} else {
		// Build target set from filters
		targetSet := make(map[string]*Client)
		
		// By user IDs
		for _, userID := range msg.Filter.UserIDs {
			if clients, ok := h.userClients[userID]; ok {
				for id, client := range clients {
					targetSet[id] = client
				}
			}
		}
		
		// By institute IDs
		for _, instID := range msg.Filter.InstituteIDs {
			for _, client := range h.clients {
				if client.InstituteID == instID {
					targetSet[client.ID] = client
				}
			}
		}
		
		// By roles
		for _, role := range msg.Filter.Roles {
			for _, client := range h.clients {
				if contains(client.Roles, role) {
					targetSet[client.ID] = client
				}
			}
		}
		
		// By group names
		for _, groupName := range msg.Filter.GroupNames {
			if clients, ok := h.groupClients[groupName]; ok {
				for id, client := range clients {
					targetSet[id] = client
				}
			}
		}
		
		// If no filters, broadcast to all
		if len(msg.Filter.UserIDs) == 0 && len(msg.Filter.InstituteIDs) == 0 && 
			len(msg.Filter.Roles) == 0 && len(msg.Filter.GroupNames) == 0 {
			for _, client := range h.clients {
				targetSet[client.ID] = client
			}
		}
		
		// Exclude specified clients
		for _, id := range msg.ExcludeIDs {
			delete(targetSet, id)
		}
		
		for _, client := range targetSet {
			targets = append(targets, client)
		}
	}
	
	// Send to targets
	for _, client := range targets {
		select {
		case client.Send <- msg.Message:
		default:
			h.logger.Warn().
				Str("client_id", client.ID).
				Msg("Client send buffer full, dropping message")
		}
	}
	
	h.logger.Debug().
		Int("targets", len(targets)).
		Str("type", msg.Message.Type).
		Msg("Message broadcast")
}

// pingClients sends ping to all clients
func (h *Hub) pingClients() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	now := time.Now()
	pingMsg := &HubMessage{
		Type: "ping",
	}
	
	for _, client := range h.clients {
		if now.Sub(client.LastPing) > h.config.PongWait {
			h.logger.Warn().
				Str("client_id", client.ID).
				Msg("Client ping timeout, closing connection")
			client.Connection.Close()
			continue
		}
		
		select {
		case client.Send <- pingMsg:
		default:
		}
	}
}

// closeAllConnections closes all client connections
func (h *Hub) closeAllConnections() {
	h.mu.Lock()
	defer h.mu.Unlock()
	
	closeMsg := &HubMessage{Type: "close"}
	
	for _, client := range h.clients {
		select {
		case client.Send <- closeMsg:
		default:
		}
		client.Connection.Close()
	}
	
	h.clients = make(map[string]*Client)
	h.userClients = make(map[string]map[string]*Client)
	h.groupClients = make(map[string]map[string]*Client)
}

// RegisterClient registers a new WebSocket connection
func (h *Hub) RegisterClient(conn *websocket.Conn, userID, instituteID string, roles []string) *Client {
	clientID := uuid.New().String()
	
	client := &Client{
		ID:            clientID,
		UserID:        userID,
		InstituteID:   instituteID,
		Roles:         roles,
		Connection:    conn,
		Send:          make(chan *HubMessage, 256),
		Groups:        make(map[string]bool),
		LastPing:      time.Now(),
		ConnectedAt:   time.Now(),
		Metadata:      make(map[string]interface{}),
	}
	
	h.register <- client
	
	// Start read/write pumps
	h.wg.Add(2)
	go h.readPump(client)
	go h.writePump(client)
	
	return client
}

// readPump handles incoming messages from client
func (h *Hub) readPump(client *Client) {
	defer h.wg.Done()
	defer func() {
		h.unregister <- client
		client.Connection.Close()
	}()
	
	client.Connection.SetReadLimit(int64(h.config.MaxMessageSize))
	client.Connection.SetReadDeadline(time.Now().Add(h.config.PongWait))
	client.Connection.SetPongHandler(func(string) error {
		client.mu.Lock()
		client.LastPing = time.Now()
		client.mu.Unlock()
		client.Connection.SetReadDeadline(time.Now().Add(h.config.PongWait))
		return nil
	})
	
	for {
		_, messageBytes, err := client.Connection.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				h.logger.Error().Err(err).Str("client_id", client.ID).Msg("WebSocket read error")
			}
			break
		}
		
		// Rate limiting
		if !h.rateLimiter.Allow(client.ID) {
			h.logger.Warn().Str("client_id", client.ID).Msg("Rate limit exceeded")
			errMsg := &HubMessage{
				Type:  "completion",
				Error: "Rate limit exceeded",
			}
			select {
			case client.Send <- errMsg:
			default:
			}
			continue
		}
		
		// Parse message
		var msg HubMessage
		if err := json.Unmarshal(messageBytes, &msg); err != nil {
			h.logger.Warn().Err(err).Str("client_id", client.ID).Msg("Failed to parse message")
			continue
		}
		
		h.handleMessage(client, &msg)
	}
}

// writePump handles outgoing messages to client
func (h *Hub) writePump(client *Client) {
	defer h.wg.Done()
	
	ticker := time.NewTicker(h.config.PingPeriod)
	defer ticker.Stop()
	
	for {
		select {
		case message, ok := <-client.Send:
			client.Connection.SetWriteDeadline(time.Now().Add(h.config.WriteWait))
			if !ok {
				client.Connection.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			
			if err := client.Connection.WriteJSON(message); err != nil {
				h.logger.Error().Err(err).Str("client_id", client.ID).Msg("WebSocket write error")
				return
			}
			
		case <-ticker.C:
			client.Connection.SetWriteDeadline(time.Now().Add(h.config.WriteWait))
			if err := client.Connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			
		case <-h.ctx.Done():
			return
		}
	}
}

// handleMessage processes incoming client messages
func (h *Hub) handleMessage(client *Client, msg *HubMessage) {
	switch msg.Type {
	case "invocation":
		h.handleInvocation(client, msg)
		
	case "ping":
		pongMsg := &HubMessage{Type: "pong"}
		select {
		case client.Send <- pongMsg:
		default:
		}
		
	case "join_group":
		h.handleJoinGroup(client, msg)
		
	case "leave_group":
		h.handleLeaveGroup(client, msg)
		
	default:
		h.logger.Debug().Str("type", msg.Type).Msg("Unhandled message type")
	}
}

// handleInvocation handles method invocations from client
func (h *Hub) handleInvocation(client *Client, msg *HubMessage) {
	// This would integrate with your RPC/notification system
	// For now, echo back with completion
	completion := &HubMessage{
		Type:          "completion",
		InvocationID:  msg.InvocationID,
		Arguments:     []interface{}{fmt.Sprintf("Echo: %v", msg.Arguments)},
	}
	
	select {
	case client.Send <- completion:
	default:
	}
}

// handleJoinGroup handles group join requests
func (h *Hub) handleJoinGroup(client *Client, msg *HubMessage) {
	if len(msg.Arguments) < 1 {
		return
	}
	
	groupName, ok := msg.Arguments[0].(string)
	if !ok {
		return
	}
	
	h.mu.Lock()
	defer h.mu.Unlock()
	
	client.mu.Lock()
	client.Groups[groupName] = true
	client.mu.Unlock()
	
	if _, exists := h.groupClients[groupName]; !exists {
		h.groupClients[groupName] = make(map[string]*Client)
	}
	h.groupClients[groupName][client.ID] = client
	
	h.logger.Debug().
		Str("client_id", client.ID).
		Str("group", groupName).
		Msg("Client joined group")
}

// handleLeaveGroup handles group leave requests
func (h *Hub) handleLeaveGroup(client *Client, msg *HubMessage) {
	if len(msg.Arguments) < 1 {
		return
	}
	
	groupName, ok := msg.Arguments[0].(string)
	if !ok {
		return
	}
	
	h.mu.Lock()
	defer h.mu.Unlock()
	
	client.mu.Lock()
	delete(client.Groups, groupName)
	client.mu.Unlock()
	
	h.removeFromGroupUnsafe(groupName, client.ID)
	
	h.logger.Debug().
		Str("client_id", client.ID).
		Str("group", groupName).
		Msg("Client left group")
}

// Public API methods for sending notifications

// SendToUser sends a message to all connections of a user
func (h *Hub) SendToUser(userID string, msg *HubMessage) {
	h.broadcast <- &BroadcastMessage{
		Message: msg,
		Filter: ClientFilter{
			UserIDs: []string{userID},
		},
	}
}

// SendToInstitute sends a message to all users in an institute
func (h *Hub) SendToInstitute(instituteID string, msg *HubMessage) {
	h.broadcast <- &BroadcastMessage{
		Message: msg,
		Filter: ClientFilter{
			InstituteIDs: []string{instituteID},
		},
	}
}

// SendToRole sends a message to all users with a specific role
func (h *Hub) SendToRole(role string, msg *HubMessage) {
	h.broadcast <- &BroadcastMessage{
		Message: msg,
		Filter: ClientFilter{
			Roles: []string{role},
		},
	}
}

// SendToGroup sends a message to all clients in a group
func (h *Hub) SendToGroup(groupName string, msg *HubMessage) {
	h.broadcast <- &BroadcastMessage{
		Message: msg,
		Filter: ClientFilter{
			GroupNames: []string{groupName},
		},
	}
}

// SendToAll broadcasts to all connected clients
func (h *Hub) SendToAll(msg *HubMessage) {
	h.broadcast <- &BroadcastMessage{
		Message: msg,
		Filter:  ClientFilter{},
	}
}

// SendNotification sends a notification to a user (SignalR-compatible)
func (h *Hub) SendNotification(userID string, event string, data interface{}) {
	msg := &HubMessage{
		Type:      "invocation",
		Target:    "NotificationReceived",
		Arguments: []interface{}{event, data},
	}
	h.SendToUser(userID, msg)
}

// GetStats returns hub statistics
func (h *Hub) GetStats() HubStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	stats := HubStats{
		TotalClients:     len(h.clients),
		TotalUsers:       len(h.userClients),
		TotalGroups:      len(h.groupClients),
		ClientsByInstitute: make(map[string]int),
		ClientsByRole:    make(map[string]int),
	}
	
	for _, client := range h.clients {
		stats.ClientsByInstitute[client.InstituteID]++
		for _, role := range client.Roles {
			stats.ClientsByRole[role]++
		}
	}
	
	return stats
}

// HubStats represents hub statistics
type HubStats struct {
	TotalClients        int
	TotalUsers          int
	TotalGroups         int
	ClientsByInstitute  map[string]int
	ClientsByRole       map[string]int
}

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}