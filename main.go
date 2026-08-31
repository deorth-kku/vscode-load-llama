// vscode-load-llama: background GUI process that watches VS Code Copilot
// chat inputs via CDP and pre-loads the selected model on the local
// llama.cpp server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"vscode-load-llama/internal/cdp"
	"vscode-load-llama/internal/config"
	"vscode-load-llama/internal/loader"
)

func main() {
	cdpAddr := flag.String("cdp", "127.0.0.1:9222", "CDP HTTP address")
	settingsPath := flag.String("settings", defaultSettingsPath(), "path to VS Code settings.json")
	cooldown := flag.Duration("cooldown", 30*time.Second, "per-model load cooldown")
	logPath := flag.String("log", defaultLogPath(), "log file path")
	verbose := flag.Bool("verbose", false, "enable debug logging")
	flag.Parse()

	if err := run(*cdpAddr, *settingsPath, *cooldown, *logPath, *verbose); err != nil {
		// GUI builds have no console; the error is also in the log file
		// (if it could be opened).
		fmt.Fprintln(os.Stderr, err)
	}
}

func run(cdpAddr, settingsPath string, cooldown time.Duration, logPath string, verbose bool) error {
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}
	defer f.Close()

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// settings.json is loaded once up front, then hot-reloaded via fsnotify.
	// The current snapshot lives in an atomic pointer inside the Store, so
	// readers never take a lock.
	store, err := config.NewStore(settingsPath, log)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	log.Info("settings loaded", "path", settingsPath, "models", len(store.Load().Models))

	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	if err := store.Watch(ctx); err != nil {
		// Non-fatal: keep monitoring with the initial snapshot.
		log.Error("start settings watcher", "err", err)
	}

	// The loader resolves the proxy per-request from the current atomic
	// snapshot, so http.proxy / http.noProxy changes hot-reload too.
	ld := loader.New(cooldown, log, store.ProxyFunc)
	events := make(chan cdp.Event, 256)
	disc := cdp.NewDiscovery(cdpAddr, events, log)
	go disc.Run(ctx)

	log.Info("monitoring", "cdp", cdpAddr, "cooldown", cooldown.String())

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return nil
		case ev := <-events:
			processEvent(ev, store, ld, log)
		}
	}
}

// processEvent handles one chat input event: skip empty input / missing
// model, look the model up in the settings table, then request a load
// (cooldown-gated inside the loader).
func processEvent(ev cdp.Event, store *config.Store, ld *loader.Loader, log *slog.Logger) {
	// Monaco inserts nbsp; normalize before the emptiness check.
	input := strings.TrimSpace(strings.ReplaceAll(ev.Input, "\u00a0", " "))
	if input == "" {
		log.Debug("skip: empty input", "window", ev.Window)
		return
	}
	if ev.Model == "" {
		log.Debug("skip: no model", "window", ev.Window)
		return
	}
	// Atomic read of the current settings snapshot (no lock).
	m, ok := store.Load().Models[ev.Model]
	if !ok {
		log.Warn("model not found in settings, skip", "model", ev.Model, "window", ev.Window)
		return
	}
	log.Info("input event", "window", ev.Window, "model", ev.Model,
		"effort", ev.Effort, "mode", ev.Mode, "inputLen", len(input))
	// Async: Load does a blocking HTTP POST (up to the client timeout).
	// A goroutine keeps the event loop responsive for all windows; the
	// mutex-gated cooldown inside the loader still dedupes concurrent
	// requests per model.
	go ld.Load(m)
}

// defaultSettingsPath returns the VS Code user settings.json location for
// the current platform:
//   - Windows: %APPDATA%\Code\User\settings.json
//   - Linux:   ~/.config/Code/User/settings.json
//   - macOS:   ~/Library/Application Support/Code/User/settings.json
func defaultSettingsPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "Code", "User", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "Code", "User", "settings.json")
}

// defaultLogPath returns the log file location in the platform temp dir,
// e.g. %TEMP%\vscode-load-llama\app.log on Windows, /tmp/... on Linux,
// $TMPDIR/... on macOS.
func defaultLogPath() string {
	return filepath.Join(os.TempDir(), "vscode-load-llama", "app.log")
}
