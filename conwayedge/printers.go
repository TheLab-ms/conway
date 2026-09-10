package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type printerConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	AccessCode   string `json:"access_code"`
	SerialNumber string `json:"serial_number"`
}

type printerStatus struct {
	SerialNumber       string          `json:"serial_number"`
	Name               string          `json:"name"`
	GcodeFile          string          `json:"gcode_file"`
	SubtaskName        string          `json:"subtask_name"`
	GcodeState         string          `json:"gcode_state"`
	PrintErrorCode     json.RawMessage `json:"print_error_code"`
	RemainingPrintTime int             `json:"remaining_print_time"`
	PrintPercentDone   int             `json:"print_percent_done"`
	UpdatedAt          int64           `json:"updated_at"`
	Error              string          `json:"error"`
}

type printerSet struct {
	lifecycle sync.Mutex
	mu        sync.RWMutex
	printers  map[string]*printer
}

type printer struct {
	config printerConfig
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	data   printerStatus
	camera *printerCamera
	// Tests substitute a child process without requiring FFmpeg or a printer.
	command func(context.Context, printerConfig) *exec.Cmd
}

type printerCamera struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	viewers map[chan []byte]struct{} // Protected by printer.mu.
}

func (s *printerSet) replace(configs []printerConfig) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	wanted := make(map[string]printerConfig, len(configs))
	for _, config := range configs {
		wanted[config.SerialNumber] = config
	}
	s.mu.Lock()
	var removed []*printer
	for serial, p := range s.printers {
		if config, ok := wanted[serial]; !ok || config != p.config {
			p.cancel()
			removed = append(removed, p)
			delete(s.printers, serial)
		}
	}
	s.mu.Unlock()
	for _, p := range removed {
		// Synchronize with camera subscriptions before waiting on their workers.
		p.mu.Lock()
		p.mu.Unlock()
		p.wg.Wait()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.printers == nil {
		s.printers = make(map[string]*printer)
	}
	for serial, config := range wanted {
		if s.printers[serial] != nil {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		p := &printer{config: config, ctx: ctx, cancel: cancel, data: printerStatus{
			SerialNumber: serial, Name: config.Name, Error: "waiting for printer status",
		}}
		s.printers[serial] = p
		p.wg.Add(1)
		go p.poll()
	}
}

func (s *printerSet) close() { s.replace(nil) }

func (s *printerSet) status(w http.ResponseWriter, r *http.Request) {
	rows := []printerStatus{}
	s.mu.RLock()
	for _, p := range s.printers {
		p.mu.Lock()
		rows = append(rows, p.data)
		p.mu.Unlock()
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].SerialNumber < rows[j].SerialNumber })
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(rows)
}

func (p *printer) report(payload []byte) {
	var message struct {
		Print map[string]json.RawMessage `json:"print"`
	}
	if json.Unmarshal(payload, &message) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.data
	next.PrintErrorCode = append(json.RawMessage(nil), next.PrintErrorCode...)
	fields := map[string]any{
		"gcode_file": &next.GcodeFile, "subtask_name": &next.SubtaskName,
		"gcode_state": &next.GcodeState, "mc_print_error_code": &next.PrintErrorCode,
		"mc_remaining_time": &next.RemainingPrintTime, "mc_percent": &next.PrintPercentDone,
	}
	changed := false
	for key, target := range fields {
		if value, ok := message.Print[key]; ok && string(value) != "null" {
			if json.Unmarshal(value, target) != nil {
				return
			}
			changed = true
		}
	}
	if changed {
		next.UpdatedAt, next.Error = time.Now().Unix(), ""
		p.data = next
	}
}

func waitMQTT(ctx context.Context, token mqtt.Token) bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	case <-token.Done():
		return token.Error() == nil
	}
}

func (p *printer) poll() {
	defer p.wg.Done()
	for p.ctx.Err() == nil {
		err := p.pollConnection()
		p.mu.Lock()
		p.data.Error = err
		p.mu.Unlock()
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (p *printer) pollConnection() string {
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	options := mqtt.NewClientOptions().
		AddBroker("ssl://" + net.JoinHostPort(p.config.Host, "8883")).
		SetClientID(fmt.Sprintf("conwayedge-%d", time.Now().UnixNano())).
		SetUsername("bblp").SetPassword(p.config.AccessCode).
		SetAutoReconnect(false).SetConnectRetry(false).SetProtocolVersion(4).
		SetConnectTimeout(5 * time.Second).SetWriteTimeout(5 * time.Second)
	options.SetCustomOpenConnectionFn(func(broker *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
		// Bambu LAN certificates are self-signed. Cancellation also interrupts CONNACK waits.
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}}
		conn, err := dialer.DialContext(ctx, "tcp", broker.Host)
		if err == nil {
			context.AfterFunc(ctx, func() { _ = conn.Close() })
		}
		return conn, err
	})
	client := mqtt.NewClient(options)
	defer func() {
		cancel()
		client.Disconnect(100)
	}()
	if !waitMQTT(ctx, client.Connect()) {
		return "MQTT connection failed"
	}
	if !waitMQTT(ctx, client.Subscribe("device/"+p.config.SerialNumber+"/report", 0, func(_ mqtt.Client, msg mqtt.Message) {
		p.report(msg.Payload())
	})) {
		return "MQTT subscription failed"
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if !waitMQTT(ctx, client.Publish("device/"+p.config.SerialNumber+"/request", 0, false,
			`{"pushing":{"command":"pushall","sequence_id":"0"}}`)) {
			return "MQTT status request failed"
		}
		p.mu.Lock()
		if time.Since(time.Unix(p.data.UpdatedAt, 0)) > 15*time.Second {
			p.data.Error = "waiting for printer status"
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return "printer stopped"
		case <-ticker.C:
		}
	}
}

const maxCameraFrame = 8 << 20

func ffmpegCommand(ctx context.Context, config printerConfig) *exec.Cmd {
	address := url.URL{Scheme: "rtsps", Host: net.JoinHostPort(config.Host, "322"),
		User: url.UserPassword("bblp", config.AccessCode), Path: "/streaming/live/1"}
	return exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-i", address.String(), "-c:v", "mjpeg", "-q:v", "5",
		"-r", "15", "-an", "-f", "mpjpeg", "-boundary_tag", "frame", "pipe:1")
}

func (p *printer) watch(ctx context.Context) (*printerCamera, chan []byte) {
	for {
		p.mu.Lock()
		if p.ctx.Err() != nil || ctx.Err() != nil {
			p.mu.Unlock()
			return nil, nil
		}
		camera := p.camera
		if camera != nil && camera.ctx.Err() != nil {
			p.mu.Unlock()
			select {
			case <-camera.done:
				continue
			case <-ctx.Done():
				return nil, nil
			case <-p.ctx.Done():
				return nil, nil
			}
		}
		if camera == nil {
			cameraCtx, cancel := context.WithCancel(p.ctx)
			camera = &printerCamera{ctx: cameraCtx, cancel: cancel, done: make(chan struct{}), viewers: make(map[chan []byte]struct{})}
			p.camera = camera
			p.wg.Add(1)
			go p.runCamera(camera)
		}
		frames := make(chan []byte, 1)
		camera.viewers[frames] = struct{}{}
		p.mu.Unlock()
		return camera, frames
	}
}

func (p *printer) unwatch(camera *printerCamera, frames chan []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(camera.viewers, frames)
	if len(camera.viewers) == 0 {
		camera.cancel()
	}
}

func (p *printer) runCamera(camera *printerCamera) {
	defer p.wg.Done()
	defer func() {
		camera.cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		for frames := range camera.viewers {
			close(frames)
		}
		p.camera = nil
		close(camera.done)
	}()
	command := p.command
	if command == nil {
		command = ffmpegCommand
	}
	cmd := command(camera.ctx, p.config)
	// Never expose stderr or exec errors: either may contain the camera password.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	defer stdout.Close()
	if cmd.Start() != nil {
		return
	}
	defer func() {
		camera.cancel()
		_ = cmd.Wait()
	}()
	reader := multipart.NewReader(stdout, "frame")
	// Kill a wedged upstream even while viewers are waiting rather than writing.
	watchdog := time.AfterFunc(20*time.Second, camera.cancel)
	defer watchdog.Stop()
	for camera.ctx.Err() == nil {
		part, err := reader.NextPart()
		if err != nil {
			return
		}
		frame, err := io.ReadAll(io.LimitReader(part, maxCameraFrame+1))
		if err != nil || len(frame) > maxCameraFrame || len(frame) < 4 ||
			frame[0] != 0xff || frame[1] != 0xd8 || frame[len(frame)-2] != 0xff || frame[len(frame)-1] != 0xd9 {
			return
		}
		watchdog.Reset(20 * time.Second)
		p.mu.Lock()
		for frames := range camera.viewers {
			// Replace a queued frame, never part of one, when a viewer falls behind.
			select {
			case <-frames:
			default:
			}
			frames <- frame
		}
		p.mu.Unlock()
	}
}

func (s *printerSet) stream(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	p := s.printers[r.PathValue("serial")]
	s.mu.RUnlock()
	if p == nil {
		http.Error(w, "printer not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	camera, frames := p.watch(ctx)
	if camera == nil {
		cancel()
		http.Error(w, "printer unavailable", http.StatusBadGateway)
		return
	}
	defer p.unwatch(camera, frames)
	var frame []byte
	select {
	case frame = <-frames:
	case <-ctx.Done():
	case <-camera.ctx.Done():
	}
	cancel()
	if frame == nil {
		http.Error(w, "camera unavailable", http.StatusBadGateway)
		return
	}
	controller := http.NewResponseController(w)
	if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
		http.Error(w, "streaming deadlines unavailable", http.StatusBadGateway)
		return
	}
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	for {
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
			return
		}
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return
		}
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil || controller.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-camera.ctx.Done():
			return
		case frame = <-frames:
			if frame == nil {
				return
			}
		}
	}
}
