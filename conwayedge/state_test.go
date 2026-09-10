package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testEdge(t *testing.T) *edge {
	t.Helper()
	e, err := openEdge(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.printers.close)
	return e
}

func request(handler http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestGoalAndController(t *testing.T) {
	e := testEdge(t)
	lan, cloud := e.routes("token", "password")
	if w := request(lan, "POST", "/api/fobs", "[]"); w.Code != 503 {
		t.Fatalf("uninitialized: %d", w.Code)
	}
	if w := request(cloud, "POST", "/api/goal", "[42,7,42]", "Authorization", "Bearer token"); w.Code != 204 {
		t.Fatalf("push: %d %s", w.Code, w.Body.String())
	}
	w := request(lan, "POST", "/api/fobs", `[{"fob":42,"allowed":true}]`)
	if w.Code != 200 || w.Body.String() != "[7,42]\n" || w.Header().Get("Content-Length") != "7" || w.Header().Get("X-Fob-Signature") != "" {
		t.Fatalf("controller response: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if len(etag) != 64 {
		t.Fatalf("ETag: %q", etag)
	}
	w = request(lan, "POST", "/api/fobs", `[{"fob":8,"allowed":false}]`, "If-None-Match", etag)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("conditional response: %d %q", w.Code, w.Body.String())
	}
	log, err := os.ReadFile(filepath.Join(e.dir, "swipes.jsonl"))
	if err != nil || strings.Count(string(log), "\n") != 2 || !strings.Contains(string(log), `"fob":8,"allowed":false`) || !strings.Contains(string(log), `"controller":"192.0.2.1"`) {
		t.Fatalf("swipe log: %s %v", log, err)
	}
	restarted, err := openEdge(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.printers.close()
	if string(restarted.body) != "[7,42]\n" || restarted.etag != etag {
		t.Fatal("restart did not restore goal")
	}
	if w := request(cloud, "POST", "/api/goal", "[]", "Authorization", "Bearer token"); w.Code != 204 {
		t.Fatalf("empty push: %d", w.Code)
	}
	w = request(lan, "POST", "/api/fobs", "[]", "If-None-Match", etag)
	if w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatalf("revocation: %d %q", w.Code, w.Body.String())
	}
}

func TestValidationAndPersistenceFailures(t *testing.T) {
	e := testEdge(t)
	goal := http.HandlerFunc(e.goal)
	request(goal, "POST", "/api/goal", "[9]")
	for _, body := range []string{"null", "{}", "[0]", "[-1]", "[4294967296]", "[1.5]", "[1] []", "[", "[" + strings.Repeat("1,", 512) + "1]", strings.Repeat(" ", 16385)} {
		if w := request(goal, "POST", "/api/goal", body); w.Code != 400 || string(e.body) != "[9]\n" {
			t.Fatalf("invalid push modified state: %d %q", w.Code, body)
		}
	}
	for _, body := range []string{"null", "[null]", "[{}]", "[] []", "[{\"fob\":-1}]"} {
		if w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", body); w.Code != 400 {
			t.Fatalf("invalid swipes accepted: %s", body)
		}
	}
	if err := os.Mkdir(filepath.Join(e.dir, "swipes.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	w := request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", `[{"fob":9,"allowed":true}]`, "If-None-Match", e.etag)
	if w.Code != 500 {
		t.Fatalf("acknowledged unlogged swipe: %d", w.Code)
	}
	blocked := filepath.Join(e.dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.dir = blocked
	if w := request(goal, "POST", "/api/goal", "[10]"); w.Code != 500 || string(e.body) != "[9]\n" {
		t.Fatal("failed write changed live goal")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goal.json")
	if err := atomicWrite(path, []byte("[1]\n")); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := atomicWrite(path, []byte("[2]\n")); err != nil {
		t.Fatal(err)
	}
	previous, _ := io.ReadAll(old)
	current, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	entries, _ := os.ReadDir(dir)
	if string(previous) != "[1]\n" || string(current) != "[2]\n" || info.Mode().Perm() != 0600 || len(entries) != 1 {
		t.Fatalf("non-atomic replacement or unsafe permissions: %q %q %v", previous, current, info.Mode())
	}
	if err := atomicWrite(dir, []byte("fail")); err == nil {
		t.Fatal("rename onto directory succeeded")
	}
	entries, _ = os.ReadDir(filepath.Dir(dir))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".swap-") {
			t.Fatal("failed write leaked temporary file")
		}
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openEdge(dir); err == nil {
		t.Fatal("corrupt startup cache silently accepted")
	}
}

func TestPersistenceRecovery(t *testing.T) {
	e := testEdge(t)
	goal := http.HandlerFunc(e.goal)
	request(goal, "POST", "/api/goal", "[1]")
	// A failed directory sync after rename can leave disk ahead of live state.
	if err := atomicWrite(filepath.Join(e.dir, "goal.json"), []byte("[2]\n")); err != nil {
		t.Fatal(err)
	}
	if w := request(goal, "POST", "/api/goal", "[1]"); w.Code != 204 {
		t.Fatalf("retry failed: %d", w.Code)
	}
	disk, _ := os.ReadFile(filepath.Join(e.dir, "goal.json"))
	if string(disk) != string(e.body) {
		t.Fatal("identical push acknowledged inconsistent disk state")
	}
	for _, partial := range []string{`{"partial":`, strings.Repeat("x", 9000)} {
		for _, prefix := range []string{"", "{\"fob\":1}\n"} {
			path := filepath.Join(e.dir, "swipes.jsonl")
			if err := os.WriteFile(path, []byte(prefix+partial), 0600); err != nil {
				t.Fatal(err)
			}
			if err := e.logSwipes([]byte("{\"fob\":2}\n")); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != prefix+"{\"fob\":2}\n" {
				t.Fatalf("partial record not repaired: %q", data)
			}
		}
	}
}

func TestRoutesAndConfig(t *testing.T) {
	e := testEdge(t)
	lan, cloud := e.routes("token", "password")
	for _, path := range []string{"/api/goal", "/api/printers", "/machines/stream/test"} {
		if w := request(cloud, "GET", path, ""); w.Code != 401 {
			t.Fatalf("tunnel auth bypass: %s %d", path, w.Code)
		}
		if w := request(lan, "GET", path, ""); w.Code != 404 {
			t.Fatalf("cloud route exposed on LAN: %s %d", path, w.Code)
		}
	}
	for _, path := range []string{"/config", "/api/fobs"} {
		if w := request(cloud, "POST", path, "[]", "Authorization", "Bearer token"); w.Code != 404 {
			t.Fatalf("LAN route exposed through tunnel: %s %d", path, w.Code)
		}
	}
	if w := request(lan, "GET", "/config", ""); w.Code != 401 {
		t.Fatal("configuration page lacks auth")
	}
	r := httptest.NewRequest("GET", "/config", nil)
	r.SetBasicAuth("admin", "password")
	auth := r.Header.Get("Authorization")
	w := request(lan, "GET", "/config", "", "Authorization", auth)
	if w.Code != 200 || !strings.Contains(w.Body.String(), e.csrf) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config page: %d %s", w.Code, w.Body.String())
	}
	for _, token := range []string{"wrong", e.csrf} {
		body := url.Values{"csrf": {token}, "printers": {"[]"}}.Encode()
		w = request(lan, "POST", "/config", body, "Authorization", auth, "Content-Type", "application/x-www-form-urlencoded")
		want := 303
		if token == "wrong" {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("config save: %d %s", w.Code, w.Body.String())
		}
	}
	config := `[{"name":"Printer","host":"127.0.0.1","access_code":"secret","serial_number":"serial"}]`
	if _, err := parsePrinters([]byte(config)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"null", "[] []", "[{}]", strings.Replace(config, "127.0.0.1", "http://localhost/", 1), strings.Replace(config, `"name"`, `"typo"`, 1)} {
		if _, err := parsePrinters([]byte(bad)); err == nil {
			t.Fatalf("invalid printer config accepted: %s", bad)
		}
	}
	form := url.Values{"csrf": {e.csrf}, "printers": {config}}.Encode()
	w = request(lan, "POST", "/config", form, "Authorization", auth, "Content-Type", "application/x-www-form-urlencoded")
	if w.Code != 303 || len(e.config) != 1 {
		t.Fatalf("printer save failed: %d %s", w.Code, w.Body.String())
	}
	e.printers.close()
	restored, err := openEdge(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.printers.close()
	if len(restored.config) != 1 || restored.config[0].AccessCode != "secret" {
		t.Fatal("printer configuration not restored")
	}
}

func TestConcurrentGoalAndLargeControllerResponse(t *testing.T) {
	e := testEdge(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			w := request(http.HandlerFunc(e.goal), "POST", "/api/goal", fmt.Sprintf("[%d]", i+1))
			if w.Code != 204 {
				t.Errorf("concurrent update: %d", w.Code)
			}
			request(http.HandlerFunc(e.fobs), "POST", "/api/fobs", "[]")
		})
	}
	wg.Wait()
	disk, _ := os.ReadFile(filepath.Join(e.dir, "goal.json"))
	if string(disk) != string(e.body) {
		t.Fatal("disk/live goal mismatch")
	}
	ids := make([]string, 512)
	for i := range ids {
		ids[i] = fmt.Sprint(4294967295 - int64(i))
	}
	request(http.HandlerFunc(e.goal), "POST", "/api/goal", "["+strings.Join(ids, ",")+"]")
	lan, _ := e.routes("token", "password")
	server := httptest.NewServer(lan)
	defer server.Close()
	response, err := http.Post(server.URL+"/api/fobs", "application/json", strings.NewReader("[]"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || len(response.TransferEncoding) != 0 || response.ContentLength != int64(len(body)) || len(body) > 6500 {
		t.Fatalf("firmware-incompatible response: %s length=%d encoding=%v", response.Status, response.ContentLength, response.TransferEncoding)
	}
}
