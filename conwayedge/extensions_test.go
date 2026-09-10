package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func restartEdge(t *testing.T, e *edge) *edge {
	t.Helper()
	e.printers.close()
	restarted, err := openEdge(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.printers.close)
	return restarted
}

func pushVersion(t *testing.T, e *edge, version int, fobs string, want int) {
	t.Helper()
	_, cloud := e.routes("token", "password")
	w := request(cloud, "POST", "/api/goal/versioned", fmt.Sprintf(`{"version":%d,"fobs":%s}`, version, fobs), "Authorization", "Bearer token")
	if w.Code != want {
		t.Fatalf("version %d: got %d, want %d: %s", version, w.Code, want, w.Body.String())
	}
}

func readSwipes(t *testing.T, e *edge, query string) []swipe {
	t.Helper()
	_, cloud := e.routes("token", "password")
	w := request(cloud, "GET", "/api/swipes"+query, "", "Authorization", "Bearer token")
	var result struct {
		Events []swipe `json:"events"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Events == nil {
		t.Fatalf("swipe fetch: %d %s", w.Code, w.Body.String())
	}
	return result.Events
}

func ack(t *testing.T, e *edge, ids []string, want int) {
	t.Helper()
	_, cloud := e.routes("token", "password")
	body, _ := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{ids})
	w := request(cloud, "POST", "/api/swipes/ack", string(body), "Authorization", "Bearer token")
	if w.Code != want {
		t.Fatalf("ack: %d %s", w.Code, w.Body.String())
	}
}

func TestVersionedGoalRestartAndLegacyInteroperation(t *testing.T) {
	e := testEdge(t)
	legacy := func(e *edge, fobs string) {
		t.Helper()
		if w := request(http.HandlerFunc(e.goal), "POST", "/api/goal", fobs); w.Code != 204 {
			t.Fatalf("legacy push: %d %s", w.Code, w.Body.String())
		}
	}
	legacy(e, "[99]")
	e = restartEdge(t, e)
	pushVersion(t, e, 0, "[42,7,42]", 204)
	pushVersion(t, e, 0, "[7,42]", 204)
	pushVersion(t, e, 0, "[7]", 409)
	pushVersion(t, e, 2, "[8]", 204)
	e = restartEdge(t, e)
	pushVersion(t, e, 1, "[9]", 409)
	pushVersion(t, e, 2, "[9]", 409)
	pushVersion(t, e, 2, "[8,8]", 204)
	legacy(e, "[100]")
	e = restartEdge(t, e)
	pushVersion(t, e, 1, "[9]", 409)
	pushVersion(t, e, 2, "[100]", 409)
	pushVersion(t, e, 2, "[8]", 204)
	if string(e.body) != "[100]\n" {
		t.Fatal("idempotent replay overwrote legacy writer")
	}
	pushVersion(t, e, 3, "[]", 204)
	e = restartEdge(t, e)
	if string(e.body) != "[]\n" || *e.version != 3 {
		t.Fatal("versioned revocation not restored")
	}
}

func TestExtensionValidationAndAuth(t *testing.T) {
	e := testEdge(t)
	lan, cloud := e.routes("token", "password")
	for _, route := range []struct{ method, path, body string }{
		{"POST", "/api/goal/versioned", `{"version":1,"fobs":[1]}`},
		{"GET", "/api/swipes", ""},
		{"POST", "/api/swipes/ack", `{"ids":[]}`},
	} {
		for _, auth := range []string{"", "Bearer wrong", "bearer token", "token", "Basic token"} {
			if w := request(cloud, route.method, route.path, route.body, "Authorization", auth); w.Code != 401 {
				t.Fatalf("auth bypass: %s %q: %d", route.path, auth, w.Code)
			}
		}
		if w := request(lan, route.method, route.path, route.body, "Authorization", "Bearer token"); w.Code != 404 {
			t.Fatalf("tunnel API on LAN: %s: %d", route.path, w.Code)
		}
		if w := request(cloud, route.method, route.path, route.body, "Authorization", "Bearer token"); w.Code >= 300 {
			t.Fatalf("authenticated request: %s: %d", route.path, w.Code)
		}
	}
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"version":null,"fobs":[]}`, `{"version":1}`, `{"version":1,"fobs":null}`,
		`{"version":-1,"fobs":[]}`, `{"version":1.5,"fobs":[]}`, `{"version":"2","fobs":[]}`,
		`{"version":9007199254740992,"fobs":[]}`, `{"version":2,"fobs":[0]}`,
		`{"version":2,"fobs":[4294967296]}`, `{"version":2,"fobs":[],"extra":1}`,
		`{"version":2,"fobs":[]} {}`, strings.Repeat(" ", 16385),
		`{"version":2,"fobs":[` + strings.Repeat("1,", 512) + `1]}`,
	} {
		if w := request(cloud, "POST", "/api/goal/versioned", body, "Authorization", "Bearer token"); w.Code != 400 {
			t.Fatalf("accepted invalid version: %q: %d", body, w.Code)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=101", "?limit=1.5", "?limit=", "?limit=no", "?limit=1&limit=2", "?limit=999999999999999999999999"} {
		if w := request(cloud, "GET", "/api/swipes"+query, "", "Authorization", "Bearer token"); w.Code != 400 {
			t.Fatalf("accepted invalid limit: %s: %d", query, w.Code)
		}
	}
	for _, body := range []string{
		`null`, `{}`, `{"ids":null}`, `{"ids":[null]}`, `{"ids":[1]}`, `{"ids":[""]}`,
		`{"ids":[],"extra":1}`, `{"ids":[]} []`, `{"ids":["` + strings.Repeat("x", 129) + `"]}`,
		`{"ids":[` + strings.Repeat(`"x",`, 100) + `"x"]}`, strings.Repeat(" ", 16385),
	} {
		if w := request(cloud, "POST", "/api/swipes/ack", body, "Authorization", "Bearer token"); w.Code != 400 {
			t.Fatalf("accepted invalid ack: %q: %d", body, w.Code)
		}
	}
	for _, handler := range []http.Handler{lan, cloud} {
		if w := request(handler, "POST", "/api/kiosk/claims", `{"fob_id":1}`, "Authorization", "Bearer token"); w.Code != 404 {
			t.Fatal("unexpected edge enrollment API")
		}
	}
}

func TestSwipeSpoolRestartAckAndBounds(t *testing.T) {
	e := testEdge(t)
	pushVersion(t, e, 1, "[7]", 204)
	lan, _ := e.routes("token", "password")
	if w := request(lan, "POST", "/api/fobs", "["+strings.Repeat(`{"fob":7,"allowed":true},`, 100)+`{"fob":8,"allowed":false}]`, "If-None-Match", e.etag); w.Code != 304 {
		t.Fatalf("conditional swipe batch: %d %s", w.Code, w.Body.String())
	}
	first := readSwipes(t, e, "")
	if len(first) != 100 || !reflect.DeepEqual(first, readSwipes(t, e, "")) {
		t.Fatal("GET consumed events or failed to bound batch")
	}
	if len(readSwipes(t, e, "?limit=1")) != 1 {
		t.Fatal("limit ignored")
	}
	for _, event := range first {
		if event.ID == "" || event.Controller != "192.0.2.1" || event.Fob != 7 || !event.Allowed || event.Time.IsZero() || event.Time.Location() != time.UTC {
			t.Fatalf("bad event: %+v", event)
		}
	}
	e = restartEdge(t, e)
	if !reflect.DeepEqual(first, readSwipes(t, e, "")) {
		t.Fatal("restart changed event IDs or order")
	}
	ack(t, e, []string{first[1].ID, "unknown", first[1].ID}, 204)
	ack(t, e, []string{first[1].ID}, 204)
	e = restartEdge(t, e)
	remaining := readSwipes(t, e, "")
	if len(remaining) != 100 || remaining[0].ID != first[0].ID || remaining[1].ID != first[2].ID || remaining[99].Fob != 8 || remaining[99].Allowed {
		t.Fatal("ack removed wrong events or lost ordering")
	}
	ids := make([]string, len(remaining))
	for i, event := range remaining {
		ids[i] = event.ID
	}
	ack(t, e, ids, 204)
	e = restartEdge(t, e)
	if len(readSwipes(t, e, "")) != 0 {
		t.Fatal("acked events resurrected")
	}
	local, err := os.ReadFile(filepath.Join(e.dir, "swipes.jsonl"))
	if err != nil || bytes.Count(local, []byte("\n")) != 101 || bytes.Contains(local, []byte(`"id":`)) {
		t.Fatal("ack modified legacy local logs")
	}
	e.swipes = make([]swipe, maxSpoolEvents)
	if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", `[{"fob":7}]`); w.Code != 503 {
		t.Fatal("full spool acknowledged new event")
	}
	if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", `[]`); w.Code != 200 {
		t.Fatal("full spool blocked empty poll")
	}
	if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "["+strings.Repeat(`{"fob":7},`, 512)+`{"fob":7}]`); w.Code != 400 {
		t.Fatal("unbounded controller batch")
	}
}

func TestExtensionWriteFailures(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, operation := range []string{"goal", "spool", "ack"} {
			t.Run(fmt.Sprintf("%s/uncertain=%t", operation, uncertain), func(t *testing.T) {
				e := testEdge(t)
				pushVersion(t, e, 1, "[1]", 204)
				if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", `[{"fob":1}]`); w.Code != 200 {
					t.Fatal(w.Code)
				}
				original := readSwipes(t, e, "")
				e.writeFile = func(path string, data []byte) error {
					if uncertain {
						if err := atomicWrite(path, data); err != nil {
							return err
						}
						return &uncertainWrite{errors.New("injected directory sync failure")}
					}
					return errors.New("injected pre-rename failure")
				}
				switch operation {
				case "goal":
					pushVersion(t, e, 3, "[3]", 500)
				case "spool":
					if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", `[{"fob":2}]`, "If-None-Match", e.etag); w.Code != 500 {
						t.Fatalf("unpersisted event acknowledged: %d", w.Code)
					}
				case "ack":
					ack(t, e, []string{original[0].ID}, 500)
				}
				if *e.version != 1 || string(e.body) != "[1]\n" || !reflect.DeepEqual(e.swipes, original) {
					t.Fatal("failed write published new live state")
				}
				e.writeFile = atomicWrite
				if uncertain {
					pushVersion(t, e, 2, "[2]", 503)
					ack(t, e, []string{}, 500)
					if w := request(http.HandlerFunc(e.goal), "POST", "/api/goal", "[9]"); w.Code != 500 {
						t.Fatal("legacy write bypassed storage fault")
					}
					if w := request(http.HandlerFunc(e.getSwipes), "GET", "/api/swipes", ""); w.Code != 503 {
						t.Fatal("served uncertain spool")
					}
					if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "[]"); w.Code != 200 || w.Body.String() != "[1]\n" {
						t.Fatal("lost last-good access")
					}
				}
				e = restartEdge(t, e)
				if uncertain && operation == "goal" {
					pushVersion(t, e, 2, "[2]", 409)
					pushVersion(t, e, 3, "[3]", 204)
				} else {
					pushVersion(t, e, 2, "[2]", 204)
				}
				wantEvents := 1
				if uncertain && operation == "spool" {
					wantEvents = 2
				}
				if uncertain && operation == "ack" {
					wantEvents = 0
				}
				if len(readSwipes(t, e, "")) != wantEvents {
					t.Fatal("restart lost committed spool state")
				}
			})
		}
	}
}

func TestConcurrentVersionsSpoolAndAck(t *testing.T) {
	e := testEdge(t)
	pushVersion(t, e, 0, "[]", 204)
	lan, cloud := e.routes("token", "password")
	var wg sync.WaitGroup
	var mu sync.Mutex
	acked := make(map[string]bool)
	for i := 1; i <= 40; i++ {
		wg.Go(func() {
			w := request(cloud, "POST", "/api/goal/versioned", fmt.Sprintf(`{"version":%d,"fobs":[%d]}`, i, i), "Authorization", "Bearer token")
			if w.Code != 204 && w.Code != 409 {
				t.Errorf("concurrent version: %d", w.Code)
			}
			w = request(lan, "POST", "/api/fobs", fmt.Sprintf(`[{"fob":%d,"allowed":true}]`, i))
			if w.Code != 200 {
				t.Errorf("concurrent swipe: %d", w.Code)
			}
			events := readSwipes(t, e, "?limit=2")
			ids := make([]string, len(events))
			for j, event := range events {
				ids[j] = event.ID
			}
			ack(t, e, ids, 204)
			mu.Lock()
			for _, id := range ids {
				acked[id] = true
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	e = restartEdge(t, e)
	if *e.version != 40 || string(e.body) != "[40]\n" {
		t.Fatal("highest concurrent version did not win")
	}
	remaining := readSwipes(t, e, "")
	for _, event := range remaining {
		if acked[event.ID] {
			t.Fatal("ack lost during concurrent append")
		}
	}
	if len(acked)+len(remaining) != 40 {
		t.Fatal("concurrent append/ack lost or duplicated events")
	}
}

func TestSigningLegacyProtocol(t *testing.T) {
	// RFC 8032 test seed/public key, stored in the engine's raw binary format.
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	public, _ := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	path := filepath.Join(t.TempDir(), "fob-signing.ed25519")
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	e := testEdge(t)
	var err error
	e.signingKey, err = loadSigningSeed(path)
	if err != nil {
		t.Fatal(err)
	}
	for version, fobs := range []string{"[42,7,42]", "[]"} {
		pushVersion(t, e, version, fobs, 204)
		w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "[]")
		sig, err := base64.StdEncoding.DecodeString(w.Header().Get("X-Fob-Signature"))
		if w.Code != 200 || err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(public, w.Body.Bytes(), sig) {
			t.Fatalf("invalid legacy signature: %d %v", w.Code, err)
		}
		if ed25519.Verify(public, bytes.TrimSpace(w.Body.Bytes()), sig) {
			t.Fatal("signature omitted trailing newline")
		}
		if w.Header().Get("Content-Length") != fmt.Sprint(w.Body.Len()) {
			t.Fatal("signed response lacks content length")
		}
		w = request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "[]", "If-None-Match", e.etag)
		if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("X-Fob-Signature") != "" {
			t.Fatal("304 differs from legacy signing protocol")
		}
	}
	e = restartEdge(t, e)
	e.signingKey, err = loadSigningSeed(path)
	if err != nil {
		t.Fatal(err)
	}
	w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "[]")
	sig, _ := base64.StdEncoding.DecodeString(w.Header().Get("X-Fob-Signature"))
	if !ed25519.Verify(public, w.Body.Bytes(), sig) {
		t.Fatal("restart changed signing identity")
	}
	for _, data := range [][]byte{nil, seed[:31], append(bytes.Clone(seed), '\n'), []byte(base64.StdEncoding.EncodeToString(seed)), ed25519.NewKeyFromSeed(seed)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSigningSeed(path); err == nil {
			t.Fatalf("accepted non-legacy seed length %d", len(data))
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := loadSigningSeed(missing); err == nil {
		t.Fatal("missing configured seed accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("generated replacement identity")
	}
	if key, err := loadSigningSeed(""); err != nil || key != nil {
		t.Fatal("optional unsigned mode broken")
	}
}

func TestCorruptExtensionStateFailsStartup(t *testing.T) {
	for _, item := range []struct{ name, data string }{
		{"goal.json", `{"version":1,"fobs":[1],"goal":[0]}`},
		{"goal.json", `{"version":null,"fobs":[1],"goal":[1]}`},
		{"goal.json", `{"version":-1,"fobs":[1],"goal":[1]}`},
		{"goal.json", `{"version":1,"fobs":null,"goal":[1]}`},
		{"swipe-spool.json", `null`},
		{"swipe-spool.json", `[{"id":"x","fob":1}]`},
		{"swipe-spool.json", `[] []`},
		{"swipe-spool.json", `[{"id":"x","time":"2026-01-01T00:00:00Z","fob":1},{"id":"x","time":"2026-01-01T00:00:00Z","fob":2}]`},
	} {
		t.Run(item.name+item.data, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, item.name), []byte(item.data), 0600); err != nil {
				t.Fatal(err)
			}
			if e, err := openEdge(dir); err == nil {
				e.printers.close()
				t.Fatal("corrupt state silently accepted")
			}
		})
	}
}
