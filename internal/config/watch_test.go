package config

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreInitialLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"oaicopilot.models":[{"id":"m1","baseUrl":"http://127.0.0.1:8080"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(p, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Load().Models["m1"]; !ok {
		t.Fatal("expected initial model m1")
	}
}

func TestStoreHotReload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	initial := `{"oaicopilot.models":[{"id":"m1","baseUrl":"http://127.0.0.1:8080"}]}`
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(p, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := st.Watch(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for the watcher to be armed before mutating.
	time.Sleep(100 * time.Millisecond)
	updated := `{"oaicopilot.models":[{"id":"m2","baseUrl":"http://10.0.0.9:8080"}]}`
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := st.Load().Models["m2"]; ok {
			return // reloaded
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("settings did not hot-reload; models=%v", st.Load().Models)
}

func TestStoreReloadKeepsPreviousOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"oaicopilot.models":[{"id":"m1","baseUrl":"http://127.0.0.1:8080"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(p, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := st.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// Corrupt the file: reload should fail and keep the previous snapshot.
	if err := os.WriteFile(p, []byte(`{not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Give the watcher time to attempt (and fail) the reload.
	time.Sleep(500 * time.Millisecond)
	if _, ok := st.Load().Models["m1"]; !ok {
		t.Fatal("expected previous snapshot to be retained after invalid reload")
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
