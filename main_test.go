package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"vscode-load-llama/internal/cdp"
	"vscode-load-llama/internal/config"
	"vscode-load-llama/internal/loader"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestProcessEvent(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, r.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()

	settings := &config.Settings{Models: map[string]config.Model{
		"Qwen3.8 27B": {ID: "Qwen3.8-27B", BaseURL: srv.URL + "/v1"},
	}}
	ld := loader.New(30*time.Second, discardLog, nil)
	log := discardLog

	// startup empty snapshot (POC-observed behavior) -> no load
	processEvent(cdp.Event{Window: "w", Input: "", Model: "Qwen3.8 27B"}, settings, ld, log)
	// nbsp/whitespace only -> no load
	processEvent(cdp.Event{Window: "w", Input: "  \u00a0 ", Model: "Qwen3.8 27B"}, settings, ld, log)
	// no model -> no load
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: ""}, settings, ld, log)
	// unknown model -> no load
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: "Unknown"}, settings, ld, log)

	mu.Lock()
	n := len(reqs)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no load requests, got %d: %v", n, reqs)
	}

	// real input -> exactly one load, at the SERVER ROOT, with the model id
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: "Qwen3.8 27B"}, settings, ld, log)
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 load request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0] != `/models/load {"model":"Qwen3.8-27B"}` {
		t.Errorf("unexpected request: %s", reqs[0])
	}
}
