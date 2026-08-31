package config

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// reloadDebounce is the quiet period after the last settings.json change
// event before a reload is triggered. VS Code (and atomic-rename writers)
// can emit several events for a single save.
const reloadDebounce = 150 * time.Millisecond

// Store holds the current Settings in an atomic pointer and can watch the
// settings file for changes, hot-reloading lock-free.
//
// Concurrency model: the watcher goroutine is the only writer and swaps the
// whole snapshot with an atomic store; every reader (the event loop, the
// loader's proxy resolver) takes an atomic load. No mutex is used anywhere,
// and a failed reload keeps the previous snapshot.
type Store struct {
	path    string
	log     *slog.Logger
	current atomic.Pointer[Settings]
}

// NewStore loads path once and returns a Store. Call Watch to start
// hot-reloading in the background.
func NewStore(path string, log *slog.Logger) (*Store, error) {
	s, err := Load(path)
	if err != nil {
		return nil, err
	}
	st := &Store{path: path, log: log}
	st.current.Store(s)
	return st, nil
}

// Load returns the current Settings snapshot (atomic read, no lock). The
// returned pointer is immutable for the lifetime of the snapshot.
func (st *Store) Load() *Settings {
	return st.current.Load()
}

func (st *Store) ProxyFunc(req *http.Request) (*url.URL, error) {
	pf := st.Load().ProxyFunc()
	if pf == nil {
		pf = http.ProxyFromEnvironment
	}
	return pf(req)
}

// Watch starts a background goroutine that watches the directory containing
// path for changes to the settings file and reloads the Store atomically.
// It returns immediately; the goroutine stops when ctx is cancelled.
//
// The directory (not the file) is watched so that atomic-rename writes
// (write temp file, rename over settings.json) are still detected, which a
// direct file watch would miss once the inode is replaced.
func (st *Store) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(filepath.Dir(st.path)); err != nil {
		w.Close()
		return err
	}
	go st.run(ctx, w)
	return nil
}

// run consumes watcher events, debounces them, and triggers reloads.
func (st *Store) run(ctx context.Context, w *fsnotify.Watcher) {
	defer w.Close()
	target := filepath.Base(st.path)

	// Debounce: (re)arm a single timer on every matching event; the timer
	// channel is stable across Reset, so it can sit in the select below.
	debounce := time.NewTimer(reloadDebounce)
	debounce.Stop()
	debounceC := debounce.C

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != target {
				continue
			}
			// Only content-affecting events matter. Remove is ignored: for an
			// atomic rename the file is re-created (Create fires) and the
			// in-place write path emits Write.
			if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Rename) {
				continue
			}
			// Drain any already-fired tick, then (re)arm the quiet window.
			if !debounce.Stop() {
				select {
				case <-debounceC:
				default:
				}
			}
			debounce.Reset(reloadDebounce)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			st.log.Warn("settings watcher error", "err", err)
		case <-debounceC:
			st.reload()
		}
	}
}

// reload re-reads the file and atomically swaps the pointer. On error the
// previous Settings are kept so a half-written or invalid file never blanks
// the model table.
func (st *Store) reload() {
	s, err := Load(st.path)
	if err != nil {
		st.log.Error("settings reload failed, keeping previous", "path", st.path, "err", err)
		return
	}
	st.current.Store(s)
	st.log.Info("settings reloaded", "path", st.path, "models", len(s.Models))
}
