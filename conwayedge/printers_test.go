package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrinterReports(t *testing.T) {
	p := &printer{data: printerStatus{SerialNumber: "serial", Name: "Printer"}}
	p.report([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"part","mc_remaining_time":42,"mc_percent":10,"mc_print_error_code":"123"}}`))
	p.report([]byte(`{"print":{"mc_percent":0,"mc_print_error_code":0}}`))
	if p.data.GcodeState != "RUNNING" || p.data.SubtaskName != "part" || p.data.RemainingPrintTime != 42 || p.data.PrintPercentDone != 0 || string(p.data.PrintErrorCode) != "0" || p.data.UpdatedAt == 0 {
		t.Fatalf("partial report lost fields: %+v", p.data)
	}
	before, _ := json.Marshal(p.data)
	for _, payload := range []string{`{`, `{"print":{"command":"pushall"}}`, `{"print":{"mc_percent":"bad","gcode_state":"BAD"}}`, `{"print":{"gcode_state":null}}`} {
		p.report([]byte(payload))
		after, _ := json.Marshal(p.data)
		if !bytes.Equal(before, after) {
			t.Fatalf("invalid/non-status report changed state: %s", payload)
		}
	}
}

func TestPrinterSetLifecycleAndStatus(t *testing.T) {
	var s printerSet
	t.Cleanup(s.close)
	w := httptest.NewRecorder()
	s.status(w, httptest.NewRequest("GET", "/", nil))
	if w.Body.String() != "[]\n" {
		t.Fatalf("zero-value status: %q", w.Body.String())
	}
	config := printerConfig{Name: "Printer", SerialNumber: "serial", Host: "127.0.0.1", AccessCode: "secret:/@password"}
	s.replace([]printerConfig{config})
	first := s.printers[config.SerialNumber]
	s.replace([]printerConfig{config})
	if s.printers[config.SerialNumber] != first || first.ctx.Err() != nil {
		t.Fatal("unchanged config restarted printer")
	}
	first.report([]byte(`{"print":{"gcode_state":"IDLE"}}`))
	w = httptest.NewRecorder()
	s.status(w, httptest.NewRequest("GET", "/", nil))
	for _, secret := range []string{config.AccessCode, config.Host, "access_code", "host"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("status exposes %q: %s", secret, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), `"gcode_state":"IDLE"`) {
		t.Fatalf("status missing print data: %s", w.Body.String())
	}
	config.Name = "Renamed"
	s.replace([]printerConfig{config})
	second := s.printers[config.SerialNumber]
	if second == first || first.ctx.Err() == nil {
		t.Fatal("changed config did not stop old printer")
	}
	s.close()
	s.close()
	if second.ctx.Err() == nil || len(s.printers) != 0 {
		t.Fatal("close did not remove and cancel printers")
	}
}

// The test executable is a controllable FFmpeg substitute, including a live process
// that must be killed and reaped rather than simply reading a finite byte buffer.
func TestPrinterCameraProcess(t *testing.T) {
	mode := os.Getenv("CONWAYEDGE_CAMERA_TEST")
	if mode == "" {
		return
	}
	if mode == "invalid" {
		fmt.Print("--frame\r\nContent-Type: image/jpeg\r\n\r\nnot a jpeg\r\n--frame--\r\n")
		os.Exit(0)
	}
	if mode == "oversized" {
		fmt.Print("--frame\r\nContent-Type: image/jpeg\r\n\r\n")
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, maxCameraFrame+1))
	} else {
		for i := byte(1); i <= 3; i++ {
			fmt.Print("--frame\r\nContent-Type: image/jpeg\r\nContent-Length: 5\r\n\r\n")
			_, _ = os.Stdout.Write([]byte{0xff, 0xd8, i, 0xff, 0xd9})
			fmt.Print("\r\n")
			time.Sleep(40 * time.Millisecond)
		}
		fmt.Print("--frame\r\n")
	}
	for {
		time.Sleep(time.Hour)
	}
}

func cameraTestPrinter(t *testing.T, mode string) (*printer, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var starts atomic.Int32
	p := &printer{ctx: ctx, cancel: cancel, config: printerConfig{SerialNumber: "camera", AccessCode: "secret-password"}}
	p.command = func(ctx context.Context, _ printerConfig) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrinterCameraProcess$")
		cmd.Env = append(os.Environ(), "CONWAYEDGE_CAMERA_TEST="+mode)
		return cmd
	}
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { p.wg.Wait(); close(done) }()
		waitCameraDone(t, done)
	})
	return p, &starts
}

func waitCameraDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("camera process/worker did not stop")
	}
}

func TestPrinterCameraSharingAndFrameDropping(t *testing.T) {
	p, starts := cameraTestPrinter(t, "frames")
	camera, fast := p.watch(context.Background())
	shared, slow := p.watch(context.Background())
	if camera != shared {
		t.Fatal("viewers do not share camera")
	}
	for {
		select {
		case frame := <-fast:
			if len(frame) != 5 || frame[0] != 0xff || frame[1] != 0xd8 || frame[3] != 0xff || frame[4] != 0xd9 {
				t.Fatalf("incomplete JPEG: %x", frame)
			}
			if frame[2] != 3 {
				continue
			}
		case <-time.After(5 * time.Second):
			t.Fatal("camera did not deliver final frame")
		}
		break
	}
	p.mu.Lock()
	frame := <-slow
	p.mu.Unlock()
	if !bytes.Equal(frame, []byte{0xff, 0xd8, 3, 0xff, 0xd9}) || starts.Load() != 1 {
		t.Fatalf("slow viewer did not get newest whole frame, frame=%x starts=%d", frame, starts.Load())
	}
	p.unwatch(camera, fast)
	if camera.ctx.Err() != nil {
		t.Fatal("camera stopped while a viewer remained")
	}
	p.unwatch(camera, slow)
	waitCameraDone(t, camera.done)
	if p.camera != nil {
		t.Fatal("camera was not released after last departure")
	}
	next, frames := p.watch(context.Background())
	if next == camera {
		t.Fatal("new viewer reused stopped camera")
	}
	p.unwatch(next, frames)
	waitCameraDone(t, next.done)
}

func TestPrinterCameraRemoval(t *testing.T) {
	p, _ := cameraTestPrinter(t, "frames")
	s := printerSet{printers: map[string]*printer{"camera": p}}
	camera, _ := p.watch(context.Background())
	s.replace(nil)
	waitCameraDone(t, camera.done)
	if p.ctx.Err() == nil {
		t.Fatal("removal did not cancel printer")
	}
	if camera, _ := p.watch(context.Background()); camera != nil {
		t.Fatal("removed printer started another camera")
	}
}

func TestPrinterCameraStall(t *testing.T) {
	p, _ := cameraTestPrinter(t, "frames")
	camera, frames := p.watch(context.Background())
	select {
	case <-frames:
	case <-time.After(5 * time.Second):
		t.Fatal("camera never started")
	}
	select {
	case <-camera.done:
	case <-time.After(25 * time.Second):
		t.Fatal("stalled camera was not killed and reaped")
	}
	p.unwatch(camera, frames)
}

func TestPrinterStreamFailures(t *testing.T) {
	for _, mode := range []string{"missing", "start-failure", "invalid", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := cameraTestPrinter(t, mode)
			s := printerSet{printers: map[string]*printer{"camera": p}}
			want := http.StatusBadGateway
			if mode == "missing" {
				s.printers = nil
				want = http.StatusNotFound
			}
			if mode == "start-failure" {
				p.command = func(ctx context.Context, _ printerConfig) *exec.Cmd {
					return exec.CommandContext(ctx, "/no-such-program/secret-password")
				}
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("serial", "camera")
			w := httptest.NewRecorder()
			s.stream(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "secret-password") {
				t.Fatalf("unsafe or incorrect failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPrinterHTTPStream(t *testing.T) {
	p, _ := cameraTestPrinter(t, "frames")
	s := printerSet{printers: map[string]*printer{"camera": p}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{serial}", s.stream)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL + "/camera")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "multipart/x-mixed-replace; boundary=frame" {
		t.Fatalf("unexpected stream response: %s %v", response.Status, response.Header)
	}
	part, err := multipart.NewReader(response.Body, "frame").NextPart()
	if err != nil || part.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("invalid HTTP multipart stream: %v", err)
	}
	frame, err := io.ReadAll(part)
	if err != nil || len(frame) != 5 || frame[0] != 0xff || frame[4] != 0xd9 {
		t.Fatalf("invalid HTTP JPEG: %x, %v", frame, err)
	}
	s.close()
}

type printerDeadlineWriter struct {
	*httptest.ResponseRecorder
	t        *testing.T
	deadline time.Time
	cancel   context.CancelFunc
	writes   int
}

func (w *printerDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *printerDeadlineWriter) Write(data []byte) (int, error) {
	if time.Until(w.deadline) <= 0 || time.Until(w.deadline) > 5*time.Second {
		w.t.Error("stream write lacks a bounded deadline")
	}
	w.deadline = time.Time{}
	w.writes++
	return w.ResponseRecorder.Write(data)
}

func (w *printerDeadlineWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *printerDeadlineWriter) Flush() {
	if time.Until(w.deadline) <= 0 || time.Until(w.deadline) > 5*time.Second {
		w.t.Error("stream flush lacks a bounded deadline")
	}
	w.ResponseRecorder.Flush()
	w.cancel()
}

func TestPrinterStreamDeadlinesAndDeparture(t *testing.T) {
	p, _ := cameraTestPrinter(t, "frames")
	s := printerSet{printers: map[string]*printer{"camera": p}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	r.SetPathValue("serial", "camera")
	w := &printerDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), t: t, cancel: cancel}
	s.stream(w, r)
	if w.writes != 3 || !w.deadline.IsZero() {
		t.Fatalf("unexpected writes or uncleared deadline: %d %v", w.writes, w.deadline)
	}
	p.mu.Lock()
	camera := p.camera
	p.mu.Unlock()
	if camera != nil {
		waitCameraDone(t, camera.done)
	}
}

func TestPrinterFFmpegURL(t *testing.T) {
	config := printerConfig{Host: "::1", AccessCode: "p@ss:/?#%"}
	cmd := ffmpegCommand(context.Background(), config)
	for i, arg := range cmd.Args {
		if arg != "-i" {
			continue
		}
		address, err := url.Parse(cmd.Args[i+1])
		if err != nil {
			t.Fatal(err)
		}
		password, _ := address.User.Password()
		if password != config.AccessCode || address.Host != "[::1]:322" || address.User.Username() != "bblp" || address.Scheme != "rtsps" {
			t.Fatalf("incorrect RTSP URL encoding: %s", address.Redacted())
		}
		return
	}
	t.Fatal("FFmpeg input argument missing")
}
