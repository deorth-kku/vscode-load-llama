package cdp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Event is a snapshot of one chat input state pushed from a window.
type Event struct {
	Window string
	Input  string
	Model  string
	Effort string
	Mode   string
}

type cdpMessage struct {
	Method string         `json:"method"`
	Params jsontext.Value `json:"params"`
}

type pageState struct {
	Input  *string `json:"input"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	Mode   *string `json:"mode"`
}

func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Session is one page-level CDP connection per VS Code window. Each
// session owns its own WebSocket so Runtime.addBinding globals never
// clash between windows.
type Session struct {
	ID string
	// title is atomically updated by the discovery scan (VS Code can
	// change a window's title at any time) and read lock-free from the
	// session goroutine (Event.Window) and from log lines.
	title atomic.Pointer[string]
	WSURL string

	events chan Event
	log    *slog.Logger

	mu   sync.Mutex
	conn *websocket.Conn

	done atomic.Bool
	last string // dedupe: last payload JSON already emitted
	n    int
}

func NewSession(id, title, wsURL string, events chan Event, log *slog.Logger) *Session {
	s := &Session{
		ID:     id,
		WSURL:  wsURL,
		events: events,
		log:    log,
	}
	s.title.Store(&title)
	return s
}

// Title returns the current window title.
func (s *Session) Title() string {
	if p := s.title.Load(); p != nil {
		return *p
	}
	return ""
}

// SetTitle atomically updates the window title (discovery scan).
func (s *Session) SetTitle(t string) { s.title.Store(&t) }

// IsDone reports whether the session's run loop has exited.
func (s *Session) IsDone() bool { return s.done.Load() }

// Stop closes the underlying WebSocket, unblocking the read loop.
func (s *Session) Stop() {
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
	}
	s.mu.Unlock()
}

// Run dials the page WebSocket and processes messages until the
// connection drops or the session is stopped. It never panics.
func (s *Session) Run(ctx context.Context) {
	defer s.done.Store(true)

	// gorilla does not send an Origin header, which is required
	// (Chrome rejects CDP WebSocket handshakes with an Origin).
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(s.WSURL, nil)
	if err != nil {
		s.log.Debug("dial failed", "window", s.Title(), "err", err)
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	defer conn.Close()

	s.log.Info("attached window", "title", s.Title())
	s.send("Runtime.enable", nil)
	s.send("Page.enable", nil)
	s.send("Runtime.addBinding", map[string]any{"name": BindingName})
	s.send("Runtime.evaluate", map[string]any{"expression": InjectJS, "returnByValue": true})

	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	conn.SetReadLimit(1 << 20)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			s.log.Debug("read loop ended", "window", s.Title(), "err", err)
			return
		}
		s.handle(raw)
	}
}

func (s *Session) handle(raw []byte) {
	var msgs []cdpMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return
		}
	} else {
		var m cdpMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return
		}
		msgs = []cdpMessage{m}
	}
	for _, m := range msgs {
		switch m.Method {
		case "Runtime.bindingCalled":
			var p struct {
				Name    string `json:"name"`
				Payload string `json:"payload"` // CDP uses "payload", not "value"
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				continue
			}
			if p.Name != BindingName || p.Payload == s.last {
				continue
			}
			s.last = p.Payload
			var st pageState
			if err := json.Unmarshal([]byte(p.Payload), &st); err != nil {
				continue
			}
			ev := Event{
				Window: s.Title(),
				Input:  strp(st.Input),
				Model:  strp(st.Model),
				Effort: strp(st.Effort),
				Mode:   strp(st.Mode),
			}
			select {
			case s.events <- ev:
			default:
				s.log.Warn("event channel full, dropping", "window", s.Title())
			}
		case "Page.frameNavigated":
			// workbench reload: the binding is gone, reinstall
			s.log.Info("frame navigated, re-injecting", "window", s.Title())
			s.send("Runtime.addBinding", map[string]any{"name": BindingName})
			s.send("Runtime.evaluate", map[string]any{"expression": InjectJS, "returnByValue": true})
		}
	}
}

func (s *Session) send(method string, params any) {
	s.n++
	m := map[string]any{"id": s.n, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.WriteMessage(websocket.TextMessage, b)
	}
}
