// Package loader sends POST {server root}/models/load requests with a
// per-model cooldown.
package loader

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"vscode-load-llama/internal/config"
)

// Loader deduplicates load requests per model id.
type Loader struct {
	cooldown time.Duration
	client   *http.Client
	log      *slog.Logger

	mu       sync.Mutex
	last     map[string]time.Time
	nonLlama map[string]struct{} // server roots proven to not be llama.cpp
}

// New creates a Loader. proxyFunc (from config.Settings.ProxyFunc)
// controls proxying of the load requests; nil means connect directly.
func New(cooldown time.Duration, log *slog.Logger, proxyFunc func(*http.Request) (*url.URL, error)) *Loader {
	client := &http.Client{Timeout: 10 * time.Second}
	if proxyFunc != nil {
		client.Transport = &http.Transport{Proxy: proxyFunc}
	}
	return &Loader{
		cooldown: cooldown,
		client:   client,
		log:      log,
		last:     make(map[string]time.Time),
		nonLlama: make(map[string]struct{}),
	}
}

// Load requests a model load if the cooldown for m.ID has expired.
// The cooldown timestamp is refreshed when the request is SENT (not on
// success), so a dead server cannot be hammered. Server roots that answer
// with a code proving they are not llama.cpp are remembered and skipped
// entirely from then on.
func (l *Loader) Load(m config.Model) {
	// The load endpoint is relative to the server ROOT, not to baseUrl:
	// baseUrl http://abc.com/v1 -> POST http://abc.com/models/load
	u, err := url.Parse(m.BaseURL)
	if err != nil {
		l.log.Error("parse baseUrl", "model", m.ID, "err", err)
		return
	}
	root := u.Scheme + "://" + u.Host
	loadURL := root + "/models/load"

	l.mu.Lock()
	if _, ok := l.nonLlama[root]; ok {
		l.mu.Unlock()
		l.log.Debug("skip: not a llama.cpp endpoint", "model", m.ID, "url", root)
		return
	}
	if last, ok := l.last[m.ID]; ok && time.Since(last) < l.cooldown {
		l.mu.Unlock()
		l.log.Debug("cooldown active, skip", "model", m.ID)
		return
	}
	l.last[m.ID] = time.Now()
	l.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"model": m.ID})
	req, err := http.NewRequest(http.MethodPost, loadURL, bytes.NewReader(body))
	if err != nil {
		l.log.Error("build request failed", "model", m.ID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		l.log.Error("models/load failed", "model", m.ID, "url", loadURL, "err", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	l.classify(m, root, loadURL, resp.StatusCode, b)
}

// classify interprets the load response and logs it accordingly.
func (l *Loader) classify(m config.Model, root, loadURL string, status int, b []byte) {
	switch {
	case status >= 200 && status < 300:
		l.log.Info("model loaded", "model", m.ID, "url", loadURL)
	case status == http.StatusBadRequest && strings.Contains(string(b), "already running"):
		// normal for llama.cpp: the model is already loaded
		l.log.Info("model already running", "model", m.ID, "url", loadURL)
	case status == http.StatusNotFound ||
		status == http.StatusMethodNotAllowed ||
		status == http.StatusUnauthorized ||
		status == http.StatusForbidden:
		// no /models/load route (or auth-walled): not llama.cpp,
		// remember the endpoint and stop trying
		l.mu.Lock()
		l.nonLlama[root] = struct{}{}
		l.mu.Unlock()
		l.log.Info("endpoint is not llama.cpp, will skip", "model", m.ID, "url", root, "status", status)
	default:
		l.log.Warn("models/load unexpected response", "model", m.ID, "url", loadURL,
			"status", status, "resp", string(b))
	}
}
