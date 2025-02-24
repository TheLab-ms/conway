package peering

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type State struct {
	Revision    int64   `json:"revision"`
	EnabledFobs []int64 `json:"enabled_fobs"`
}

type Event struct {
	UID       string `json:"uid"`
	Timestamp int64  `json:"timestamp"` // UTC unix epoch seconds

	// Only one field can be set per event
	FobSwipe *FobSwipeEvent `json:"fob_swipe"`
}

type FobSwipeEvent struct {
	FobID int64 `json:"fob_id"`
}

type Client struct {
	baseURL, token, stateDir string
	StateTransitions         chan struct{}
}

func NewClient(baseURL, token, stateDir string) *Client {
	if err := os.MkdirAll(filepath.Join(stateDir, "events"), 0755); err != nil {
		panic(err)
	}
	return &Client{baseURL, token, stateDir, make(chan struct{}, 2)}
}

func (c *Client) GetState() *State {
	state := &State{}
	f, err := os.Open(filepath.Join(c.stateDir, "state.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		slog.Error("unexpected error while reading cached state", "error", err)
		return nil
	}
	defer f.Close()

	err = json.NewDecoder(f).Decode(state)
	if err != nil {
		slog.Error("unexpected error while parsing cached state", "error", err)
		return nil
	}
	return state
}

func (c *Client) WarmCache() error {
	var after int64
	state := c.GetState()
	if state != nil {
		after = state.Revision
	}

	// Roundtrip to the server
	resp, err := c.roundtrip(http.MethodGet, fmt.Sprintf("/api/peering/state?after=%d", after), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil // state hasn't changed since we last saw it
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Write the response to a temp file
	tmpPath := filepath.Join(c.stateDir, ".state.json")
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		file.Close()
		return err
	}
	file.Close()

	// Swap the temp file into place (atomic)
	err = os.Rename(tmpPath, filepath.Join(c.stateDir, "state.json"))
	if err != nil {
		return err
	}
	slog.Info("updated cache from Conway")

	// Signal the state transition if someone is listening
	select {
	case c.StateTransitions <- struct{}{}:
	default:
	}

	return nil
}

var eventLock sync.Mutex

func (c *Client) BufferEvent(event *Event) {
	eventLock.Lock()
	defer eventLock.Unlock()

	js, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}

	tmp := filepath.Join(c.stateDir, "events", ".tmp")
	fp := filepath.Join(c.stateDir, "events", time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(tmp, js, 0644); err != nil {
		panic(fmt.Sprintf("buffering event to disk: %s", err))
	}
	if err := os.Rename(tmp, fp); err != nil {
		panic(fmt.Sprintf("swapping temp event file: %s", err))
	}

	time.Sleep(time.Nanosecond) // dirty hack to make sure every timestamp is unique
}

func (c *Client) FlushEvents() error {
	filenames := []string{}
	events := [][]byte{}

	// Read the buffered events from disk (if any)
	files, err := os.ReadDir(filepath.Join(c.stateDir, "events"))
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == ".tmp" {
			continue
		}
		fullPath := filepath.Join(c.stateDir, "events", file.Name())
		js, err := os.ReadFile(fullPath)
		if err != nil {
			return err
		}
		events = append(events, js)
		filenames = append(filenames, fullPath)
		if len(events) >= 100 {
			break // limit the batch size
		}
	}

	if len(files) == 0 {
		return nil // nothing to do
	}

	// Write the events to the server
	resp, err := c.roundtrip(http.MethodPost, "/api/peering/events", bytes.NewReader(bytes.Join(events, []byte("\n"))))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	slog.Info("flushed events to server", "count", len(events))

	// Remove the files that were successfully sent
	for _, name := range filenames {
		err = os.Remove(name)
		if err != nil {
			return err
		}
	}

	return nil
}

var client = &http.Client{Timeout: 5 * time.Second}

func (c *Client) roundtrip(method, path string, body io.Reader) (*http.Response, error) {
	uri := fmt.Sprintf("%s/%s", c.baseURL, path)
	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	return client.Do(req)
}
