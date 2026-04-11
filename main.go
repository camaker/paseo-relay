// Paseo relay server — zero-knowledge WebSocket relay.
//
// The relay bridges Paseo daemon and mobile app connections without being
// able to read the traffic. All messages are E2E encrypted by the clients;
// the relay only forwards bytes.
//
// Protocol (v2):
//
//	GET /ws?serverId=<id>&role=server&v=2                          — daemon control socket
//	GET /ws?serverId=<id>&role=server&connectionId=<c>&v=2         — daemon data socket
//	GET /ws?serverId=<id>&role=client[&connectionId=<c>]&v=2       — client socket
//
// GET /health — returns {"status":"ok","version":"<version>"}
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// version is set at build time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

// ---- WebSocket upgrader ----

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin:      func(r *http.Request) bool { return true },
}

// ---- Thread-safe WebSocket connection ----

// conn wraps a gorilla WebSocket with a write mutex.
// gorilla allows one concurrent reader and one concurrent writer;
// reads are always done from a single goroutine per conn, so only
// writes need to be serialized.
type conn struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func newConn(ws *websocket.Conn) *conn { return &conn{ws: ws} }

func (c *conn) send(msgType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(msgType, data)
}

func (c *conn) close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msg := websocket.FormatCloseMessage(code, reason)
	_ = c.ws.WriteMessage(websocket.CloseMessage, msg)
	_ = c.ws.Close()
}

// ---- Frame buffer ----

type frameBuffer struct {
	mu       sync.Mutex
	frames   [][]byte
	maxItems int
}

func newFrameBuffer(maxItems int) *frameBuffer {
	return &frameBuffer{maxItems: maxItems}
}

// push stores a frame. Each frame is prefixed with a 1-byte msgType so it can
// be replayed with the correct WebSocket message type.
func (b *frameBuffer) push(msgType int, data []byte) {
	frame := make([]byte, 1+len(data))
	frame[0] = byte(msgType)
	copy(frame[1:], data)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, frame)
	if len(b.frames) > b.maxItems {
		b.frames = b.frames[len(b.frames)-b.maxItems:]
	}
}

// flush returns all buffered frames and clears the buffer.
func (b *frameBuffer) flush() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.frames
	b.frames = nil
	return out
}

// ---- Session ----

type session struct {
	mu sync.RWMutex

	// daemon control socket (one per session)
	control *conn

	// daemon data sockets keyed by connectionId
	serverData map[string]*conn

	// client sockets keyed by connectionId (multiple allowed per connectionId)
	clients map[string][]*conn

	// frames buffered while the daemon data socket hasn't arrived yet
	pending map[string]*frameBuffer

	maxBufferFrames int
}

func newSession(maxBufferFrames int) *session {
	return &session{
		serverData:      make(map[string]*conn),
		clients:         make(map[string][]*conn),
		pending:         make(map[string]*frameBuffer),
		maxBufferFrames: maxBufferFrames,
	}
}

func (s *session) isEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.control == nil && len(s.serverData) == 0 && len(s.clients) == 0
}

func (s *session) connectedConnectionIds() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.clients))
	for id := range s.clients {
		ids = append(ids, id)
	}
	return ids
}

func (s *session) notifyControl(msg any) {
	s.mu.RLock()
	ctl := s.control
	s.mu.RUnlock()
	if ctl == nil {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if err := ctl.send(websocket.TextMessage, data); err != nil {
		ctl.close(1011, "Control send failed")
	}
}

func (s *session) bufferFrame(connectionId string, msgType int, data []byte) {
	s.mu.Lock()
	buf, ok := s.pending[connectionId]
	if !ok {
		buf = newFrameBuffer(s.maxBufferFrames)
		s.pending[connectionId] = buf
	}
	s.mu.Unlock()
	buf.push(msgType, data)
}

func (s *session) flushFrames(connectionId string, dst *conn) {
	s.mu.Lock()
	buf := s.pending[connectionId]
	delete(s.pending, connectionId)
	s.mu.Unlock()
	if buf == nil {
		return
	}
	for _, frame := range buf.flush() {
		if len(frame) == 0 {
			continue
		}
		_ = dst.send(int(frame[0]), frame[1:])
	}
}

// nudgeOrResetControl watches whether the daemon opens a data socket for
// connectionId after a client connects. If not, it nudges the control socket
// with a sync message; if still no reaction, force-closes the control socket
// so the daemon reconnects.
func (s *session) nudgeOrResetControl(connectionId string) {
	time.AfterFunc(10*time.Second, func() {
		s.mu.RLock()
		hasClient := len(s.clients[connectionId]) > 0
		_, hasData := s.serverData[connectionId]
		s.mu.RUnlock()

		if !hasClient || hasData {
			return
		}
		s.notifyControl(map[string]any{
			"type":          "sync",
			"connectionIds": s.connectedConnectionIds(),
		})

		time.AfterFunc(5*time.Second, func() {
			s.mu.RLock()
			hasClient2 := len(s.clients[connectionId]) > 0
			_, hasData2 := s.serverData[connectionId]
			ctl := s.control
			s.mu.RUnlock()

			if !hasClient2 || hasData2 || ctl == nil {
				return
			}
			ctl.close(1011, "Control unresponsive")
		})
	})
}

// ---- Session registry ----

type registry struct {
	mu              sync.Mutex
	sessions        map[string]*session
	maxBufferFrames int
}

func newRegistry(maxBufferFrames int) *registry {
	return &registry{
		sessions:        make(map[string]*session),
		maxBufferFrames: maxBufferFrames,
	}
}

func (r *registry) get(serverId string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[serverId]
	if !ok {
		s = newSession(r.maxBufferFrames)
		r.sessions[serverId] = s
	}
	return s
}

// evict removes sessions that have no active connections.
func (r *registry) evict() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.sessions {
		if s.isEmpty() {
			delete(r.sessions, id)
		}
	}
}

// startEvictionLoop runs a background goroutine that periodically evicts
// empty sessions to prevent unbounded memory growth.
func (r *registry) startEvictionLoop(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.evict()
			}
		}
	}()
}

// ---- HTTP handler ----

type relayHandler struct {
	reg *registry
}

func (h *relayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(map[string]string{"status": "ok", "version": version})
		_, _ = w.Write(data)
	case "/ws":
		h.handleWS(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *relayHandler) handleWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	serverId := q.Get("serverId")
	role := q.Get("role")
	connectionId := q.Get("connectionId")
	v := q.Get("v")

	if serverId == "" {
		http.Error(w, "Missing serverId", http.StatusBadRequest)
		return
	}
	if role != "server" && role != "client" {
		http.Error(w, "Invalid role (expected server or client)", http.StatusBadRequest)
		return
	}
	if v != "2" {
		http.Error(w, "Only v=2 is supported", http.StatusBadRequest)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "err", err)
		return
	}

	sess := h.reg.get(serverId)
	c := newConn(ws)

	switch role {
	case "server":
		if connectionId == "" {
			h.handleServerControl(sess, c, serverId)
		} else {
			h.handleServerData(sess, c, serverId, connectionId)
		}
	case "client":
		if connectionId == "" {
			connectionId = "conn_" + randomHex(8)
		}
		h.handleClient(sess, c, serverId, connectionId)
	}
}

// handleServerControl manages the daemon's control socket (one per session).
func (h *relayHandler) handleServerControl(sess *session, c *conn, serverId string) {
	slog.Info("server-control connected", "serverId", serverId)

	sess.mu.Lock()
	old := sess.control
	sess.control = c
	sess.mu.Unlock()
	if old != nil {
		old.close(1008, "Replaced by new connection")
	}

	// Send current connection list so the daemon can attach existing clients.
	ids := sess.connectedConnectionIds()
	data, _ := json.Marshal(map[string]any{"type": "sync", "connectionIds": ids})
	_ = c.send(websocket.TextMessage, data)

	defer func() {
		sess.mu.Lock()
		if sess.control == c {
			sess.control = nil
		}
		sess.mu.Unlock()
		slog.Info("server-control disconnected", "serverId", serverId)
	}()

	for {
		msgType, msg, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		// Respond to ping keepalives on the control channel.
		if msgType == websocket.TextMessage {
			var parsed map[string]any
			if json.Unmarshal(msg, &parsed) == nil {
				if parsed["type"] == "ping" {
					resp, _ := json.Marshal(map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
					_ = c.send(websocket.TextMessage, resp)
					continue
				}
			}
		}
		// Control channel carries no data frames; ignore anything else.
	}
}

// handleServerData manages a daemon data socket for a specific connectionId.
func (h *relayHandler) handleServerData(sess *session, c *conn, serverId, connectionId string) {
	slog.Info("server-data connected", "serverId", serverId, "connectionId", connectionId)

	sess.mu.Lock()
	old := sess.serverData[connectionId]
	sess.serverData[connectionId] = c
	sess.mu.Unlock()
	if old != nil {
		old.close(1008, "Replaced by new connection")
	}

	// Flush frames that arrived before the daemon connected.
	sess.flushFrames(connectionId, c)

	defer func() {
		sess.mu.Lock()
		if sess.serverData[connectionId] == c {
			delete(sess.serverData, connectionId)
		}
		clients := append([]*conn(nil), sess.clients[connectionId]...)
		sess.mu.Unlock()

		// Force clients to reconnect and re-handshake when the daemon drops.
		for _, cl := range clients {
			cl.close(1012, "Server disconnected")
		}
		slog.Info("server-data disconnected", "serverId", serverId, "connectionId", connectionId)
	}()

	for {
		msgType, msg, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		sess.mu.RLock()
		targets := append([]*conn(nil), sess.clients[connectionId]...)
		sess.mu.RUnlock()
		for _, cl := range targets {
			if err := cl.send(msgType, msg); err != nil {
				slog.Error("forward server->client failed", "connectionId", connectionId, "err", err)
			}
		}
	}
}

// handleClient manages an app/client socket (multiple allowed per connectionId).
func (h *relayHandler) handleClient(sess *session, c *conn, serverId, connectionId string) {
	slog.Info("client connected", "serverId", serverId, "connectionId", connectionId)

	sess.mu.Lock()
	sess.clients[connectionId] = append(sess.clients[connectionId], c)
	sess.mu.Unlock()

	sess.notifyControl(map[string]any{"type": "connected", "connectionId": connectionId})
	sess.nudgeOrResetControl(connectionId)

	defer func() {
		sess.mu.Lock()
		list := sess.clients[connectionId]
		newList := make([]*conn, 0, len(list))
		for _, cl := range list {
			if cl != c {
				newList = append(newList, cl)
			}
		}
		isLast := len(newList) == 0
		if isLast {
			delete(sess.clients, connectionId)
			delete(sess.pending, connectionId)
		} else {
			sess.clients[connectionId] = newList
		}
		srv := sess.serverData[connectionId]
		sess.mu.Unlock()

		if isLast {
			if srv != nil {
				srv.close(1001, "Client disconnected")
			}
			sess.notifyControl(map[string]any{"type": "disconnected", "connectionId": connectionId})
		}
		slog.Info("client disconnected", "serverId", serverId, "connectionId", connectionId)
	}()

	for {
		msgType, msg, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		sess.mu.RLock()
		srv := sess.serverData[connectionId]
		sess.mu.RUnlock()

		if srv == nil {
			sess.bufferFrame(connectionId, msgType, msg)
			continue
		}
		if err := srv.send(msgType, msg); err != nil {
			slog.Error("forward client->server failed", "connectionId", connectionId, "err", err)
		}
	}
}

// ---- Helpers ----

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// fallback: time-based
		t := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(t >> (i * 8))
		}
	}
	const hx = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = hx[v>>4]
		out[i*2+1] = hx[v&0xf]
	}
	return string(out)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---- Main ----

func main() {
	addr := flag.String("addr", envOrDefault("RELAY_ADDR", ":8411"), "listen address")
	maxBuf := flag.Int("max-buffer-frames", 200, "max frames buffered per connection while daemon is connecting")
	logFormat := flag.String("log-format", envOrDefault("LOG_FORMAT", "text"), "log format: text or json")
	printVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Configure structured logging.
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if *logFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := newRegistry(*maxBuf)
	reg.startEvictionLoop(ctx, 60*time.Second)

	srv := &http.Server{
		Addr:    *addr,
		Handler: &relayHandler{reg: reg},
	}

	go func() {
		slog.Info("paseo relay starting", "addr", *addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
	slog.Info("stopped")
}
