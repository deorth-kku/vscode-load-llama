package loader

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"vscode-load-llama/internal/config"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLoadPostsToServerRoot(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	// baseUrl carries a path that must be STRIPPED: the load endpoint
	// is relative to the server root, not to baseUrl.
	m := config.Model{ID: "Qwen3.8-27B", BaseURL: srv.URL + "/v1"}
	l.Load(m)

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("expected 1 request, got %d", len(paths))
	}
	if paths[0] != "/models/load" {
		t.Errorf("expected path /models/load (server root), got %s", paths[0])
	}
	if bodies[0] != `{"model":"Qwen3.8-27B"}` {
		t.Errorf("unexpected body: %s", bodies[0])
	}
}

func TestLoadCooldownSkips(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	m := config.Model{ID: "m1", BaseURL: srv.URL}
	l.Load(m)
	l.Load(m) // within cooldown -> skipped
	if n != 1 {
		t.Fatalf("expected 1 request after cooldown skip, got %d", n)
	}
}

func TestLoadDifferentModelsIndependent(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	l.Load(config.Model{ID: "m1", BaseURL: srv.URL})
	l.Load(config.Model{ID: "m2", BaseURL: srv.URL})
	if n != 2 {
		t.Fatalf("expected 2 requests for different models, got %d", n)
	}
}

func TestLoadMarksNonLlamaAndSkips(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusNotFound) // no /models/load route
	}))
	defer srv.Close()

	// short cooldown so the 2nd/3rd calls are NOT blocked by cooldown
	l := New(10*time.Millisecond, discardLog, nil)
	m := config.Model{ID: "m1", BaseURL: srv.URL}
	l.Load(m)
	time.Sleep(20 * time.Millisecond)
	l.Load(m) // must be skipped via the non-llama set, not cooldown
	time.Sleep(20 * time.Millisecond)
	l.Load(m)
	if n != 1 {
		t.Fatalf("expected 1 request after non-llama marking, got %d", n)
	}
}

func TestLoadAlreadyRunningIsNormal(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	// short cooldown: if "already running" wrongly marked the endpoint
	// as non-llama, the 2nd call would be skipped and n would stay 1.
	l := New(10*time.Millisecond, discardLog, nil)
	m := config.Model{ID: "m1", BaseURL: srv.URL}
	l.Load(m)
	time.Sleep(20 * time.Millisecond)
	l.Load(m)
	if n != 2 {
		t.Fatalf("expected 2 requests (already running is normal), got %d", n)
	}
}

func TestLoadRespectsProxy(t *testing.T) {
	var targetN, proxyN int
	// target llama server
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetN++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	// minimal forwarding proxy (absolute-URI form)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyN++
		req2, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(req2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	l := New(30*time.Second, discardLog, func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	l.Load(config.Model{ID: "m1", BaseURL: target.URL})

	if proxyN != 1 {
		t.Fatalf("expected request to go through the proxy, proxy saw %d", proxyN)
	}
	if targetN != 1 {
		t.Fatalf("expected target to receive the forwarded request, got %d", targetN)
	}
}
