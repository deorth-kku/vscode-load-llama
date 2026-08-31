package cdp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startMockPage simulates a VS Code workbench page over CDP. It records
// every command method the client sends (in order) and drives the same
// flow the real page produces:
//
//  1. client: Runtime.enable, Page.enable, Runtime.addBinding, Runtime.evaluate
//  2. page:   bindingCalled with EMPTY input (startup snapshot — the
//     real page always emits this on (re)inject),
//     then a real input event,
//     then Page.frameNavigated (workbench reload),
//  3. client: Runtime.addBinding, Runtime.evaluate (re-injection)
//  4. page:   bindingCalled with final input
func startMockPage(t *testing.T) (wsURL string, methods chan string) {
	t.Helper()
	methods = make(chan string, 32)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(payload string) {
			conn.WriteJSON(map[string]any{
				"method": "Runtime.bindingCalled",
				"params": map[string]any{"name": BindingName, "payload": payload},
			})
		}
		evaluates := 0
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			method, _ := m["method"].(string)
			methods <- method
			if method == "Runtime.evaluate" {
				evaluates++
				if evaluates == 1 {
					// startup snapshot: empty input (POC-observed behavior)
					send(`{"input":"","model":"Qwen3.8 27B","effort":null,"mode":null}`)
					send(`{"input":"hello","model":"Qwen3.8 27B","effort":"high","mode":"agent"}`)
					// simulate workbench reload
					conn.WriteJSON(map[string]any{"method": "Page.frameNavigated"})
				} else {
					// re-injection after frameNavigated
					send(`{"input":"final","model":"Qwen3.8 27B","effort":null,"mode":null}`)
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), methods
}

func readEvent(t *testing.T, events chan Event) Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
		return Event{}
	}
}

func TestSessionHandshakeAndEvents(t *testing.T) {
	wsURL, methods := startMockPage(t)
	events := make(chan Event, 10)
	s := NewSession("test-id", "Test Window", wsURL, events, discardLog)
	go s.Run(context.Background())

	// 1. handshake commands in the expected order
	for _, want := range []string{"Runtime.enable", "Page.enable", "Runtime.addBinding", "Runtime.evaluate"} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("command order: got %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for handshake command")
		}
	}

	// 2. startup empty-input snapshot is forwarded (the POC-verified case)
	ev := readEvent(t, events)
	if ev.Input != "" {
		t.Errorf("first event should be the startup empty snapshot, got %q", ev.Input)
	}
	if ev.Model != "Qwen3.8 27B" || ev.Window != "Test Window" {
		t.Errorf("unexpected first event: %+v", ev)
	}

	// 3. real input event forwarded with all fields
	ev = readEvent(t, events)
	if ev.Input != "hello" || ev.Effort != "high" || ev.Mode != "agent" {
		t.Errorf("unexpected second event: %+v", ev)
	}

	// 4. frameNavigated triggers re-injection (addBinding + evaluate again)
	for _, want := range []string{"Runtime.addBinding", "Runtime.evaluate"} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("re-injection: got %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for re-injection command")
		}
	}

	// 5. event after re-injection is delivered
	ev = readEvent(t, events)
	if ev.Input != "final" {
		t.Errorf("unexpected third event: %+v", ev)
	}

	s.Stop()
}

func TestSessionDialFailureDoesNotPanic(t *testing.T) {
	events := make(chan Event, 1)
	s := NewSession("x", "X", "ws://127.0.0.1:1/devtools/page/x", events, discardLog)
	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
		if !s.IsDone() {
			t.Error("IsDone should be true after run exits")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after dial failure")
	}
}
