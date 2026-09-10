package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

type edge struct {
	mu             sync.Mutex
	dir            string
	body           []byte
	etag           string
	config         []printerConfig
	csrf           string
	printers       printerSet
	version        *int64
	versionBody    []byte
	swipes         []swipe
	signingKey     ed25519.PrivateKey
	persistenceErr error
	writeFile      func(string, []byte) error
}

func openEdge(dir string) (*edge, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	e := &edge{dir: dir, config: []printerConfig{}, csrf: rand.Text(), swipes: []swipe{}, writeFile: atomicWrite}
	for _, name := range []string{"goal.json", "printers.json", "swipe-spool.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			if name == "goal.json" {
				if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
					var state goalState
					err = decodeJSON(data, &state)
					if err == nil {
						e.version, e.versionBody, err = parseVersionedGoal(state.Version, state.Fobs)
					}
					if err == nil {
						e.body, e.etag, err = parseGoal(state.Goal)
					}
				} else {
					e.body, e.etag, err = parseGoal(data)
				}
			} else if name == "swipe-spool.json" {
				err = decodeJSON(data, &e.swipes)
				if err == nil {
					err = validateSpool(e.swipes)
				}
			} else {
				e.config, err = parsePrinters(data)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
	}
	// A process restart may follow a failed post-rename sync rather than a
	// machine reboot. Finish that sync before treating loaded state as durable.
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sync loaded state: %w", err)
	}
	e.printers.replace(e.config)
	return e, nil
}

// The temporary file must share the destination filesystem for an atomic rename.
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".swap-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return &uncertainWrite{err}
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return &uncertainWrite{err}
	}
	return nil
}

// Rename succeeded, but durability is unknown. Never overwrite that state from
// stale memory; restart must reconcile it before accepting more mutations.
type uncertainWrite struct{ error }

func (e *edge) persist(name string, data []byte) error {
	if e.persistenceErr != nil {
		return e.persistenceErr
	}
	err := e.writeFile(filepath.Join(e.dir, name), data)
	var uncertain *uncertainWrite
	if errors.As(err, &uncertain) {
		e.persistenceErr = err
	}
	return err
}

func parseGoal(data []byte) ([]byte, string, error) {
	var ids []uint32
	if err := json.Unmarshal(data, &ids); err != nil || ids == nil || len(ids) > 512 || slices.Contains(ids, 0) {
		return nil, "", fmt.Errorf("goal must be an array of at most 512 nonzero uint32 fob IDs")
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	body, _ := json.Marshal(ids)
	body = append(body, '\n')
	hash := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(hash, "%d,", id)
	}
	return body, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (e *edge) goal(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		http.Error(w, "cannot read goal (limit 16 KiB)", http.StatusBadRequest)
		return
	}
	body, etag, err := parseGoal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.persistGoal(body, e.version, e.versionBody); err != nil {
		log.Printf("persist goal: %v", err)
		http.Error(w, "cannot persist goal", http.StatusInternalServerError)
		return
	}
	e.body, e.etag = body, etag
	w.WriteHeader(http.StatusNoContent)
}

func (e *edge) fobs(w http.ResponseWriter, r *http.Request) {
	var events []struct {
		Fob     uint32 `json:"fob"`
		Allowed bool   `json:"allowed"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || json.Unmarshal(data, &events) != nil || events == nil || len(events) > 512 {
		http.Error(w, "invalid swipe array", http.StatusBadRequest)
		return
	}
	for _, event := range events {
		if event.Fob == 0 {
			http.Error(w, "invalid fob ID", http.StatusBadRequest)
			return
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.body == nil {
		http.Error(w, "no goal received yet", http.StatusServiceUnavailable)
		return
	}
	if len(events) != 0 {
		if e.persistenceErr != nil || len(e.swipes)+len(events) > maxSpoolEvents {
			http.Error(w, "swipe storage unavailable; retry later", http.StatusServiceUnavailable)
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		var entries bytes.Buffer
		pending := slices.Clone(e.swipes)
		for _, event := range events {
			entry := swipe{rand.Text(), time.Now().UTC(), ip, event.Fob, event.Allowed}
			pending = append(pending, entry)
			_ = json.NewEncoder(&entries).Encode(struct {
				Time       time.Time `json:"time"`
				Controller string    `json:"controller"`
				Fob        uint32    `json:"fob"`
				Allowed    bool      `json:"allowed"`
			}{entry.Time, ip, event.Fob, event.Allowed})
		}
		if err := e.logSwipes(entries.Bytes()); err != nil {
			log.Printf("persist swipes: %v", err)
			http.Error(w, "cannot persist swipes", http.StatusInternalServerError)
			return
		}
		data, _ := json.Marshal(pending)
		if err := e.persist("swipe-spool.json", data); err != nil {
			log.Printf("persist swipe spool: %v", err)
			http.Error(w, "cannot persist swipe spool", http.StatusInternalServerError)
			return
		}
		e.swipes = pending
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == e.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", e.etag)
	if e.signingKey != nil {
		w.Header().Set("X-Fob-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(e.signingKey, e.body)))
	}
	// Firmware does not decode chunked transfer encoding.
	w.Header().Set("Content-Length", strconv.Itoa(len(e.body)))
	_, _ = w.Write(e.body)
}

// Called with e.mu held: each acknowledged batch has reached disk.
func (e *edge) logSwipes(data []byte) error {
	f, err := os.OpenFile(filepath.Join(e.dir, "swipes.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// Remove an interrupted, unacknowledged final record before appending.
	size := info.Size()
	var buf [4096]byte
	for end := size; end > 0; {
		start := max(0, end-int64(len(buf)))
		chunk := buf[:end-start]
		if _, err := f.ReadAt(chunk, start); err != nil {
			return err
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			size = start + int64(i) + 1
			break
		}
		end, size = start, start
	}
	if size != info.Size() {
		if err := f.Truncate(size); err != nil {
			return err
		}
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err != nil {
		_ = f.Truncate(size)
		_ = f.Sync()
		return err
	}
	dir, err := os.Open(e.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
