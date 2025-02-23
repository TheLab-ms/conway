package main

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

	"github.com/TheLab-ms/conway/modules/api"
)

func getState(conf *Config, after int64) (*api.GliderState, error) {
	resp, err := roundtripToConway(conf, http.MethodGet, fmt.Sprintf("/api/glider/state?after=%d", after), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil // state hasn't changed since we last saw it
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	state := &api.GliderState{}
	return state, json.NewDecoder(resp.Body).Decode(state)
}

var eventLock sync.Mutex

func bufferEvent(conf *Config, event *api.GliderEvent) {
	eventLock.Lock()
	defer eventLock.Unlock()

	js, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}

	tmp := filepath.Join(conf.StateDir, "events", ".tmp")
	fp := filepath.Join(conf.StateDir, "events", time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(tmp, js, 0644); err != nil {
		panic(fmt.Sprintf("buffering event to disk: %s", err))
	}
	if err := os.Rename(tmp, fp); err != nil {
		panic(fmt.Sprintf("swapping temp event file: %s", err))
	}

	time.Sleep(time.Nanosecond) // dirty hack to make sure every timestamp is unique
}

func flushEvents(conf *Config) error {
	filenames := []string{}
	events := [][]byte{}

	// Read the buffered events from disk (if any)
	files, err := os.ReadDir(filepath.Join(conf.StateDir, "events"))
	if err != nil {
		return err
	}
	for _, file := range files {
		fullPath := filepath.Join(conf.StateDir, "events", file.Name())
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

	// Write the events to the server
	resp, err := roundtripToConway(conf, http.MethodPost, "/api/glider/events", bytes.NewReader(bytes.Join(events, []byte("\n"))))
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

func roundtripToConway(conf *Config, method, path string, body io.Reader) (*http.Response, error) {
	uri := fmt.Sprintf("%s/%s", conf.ConwayURL, path)
	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+conf.ConwayToken)

	return client.Do(req)
}
