package bambu

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	mqttPort       = 8883
	mqttQoS        = 0
	connectTimeout = 5 * time.Second
	dataTimeout    = 10 * time.Second
)

type PrinterConfig struct {
	Host         string
	AccessCode   string
	SerialNumber string
}

type PrinterData struct {
	GcodeFile          string // Current gcode filename
	SubtaskName        string // User-editable plate name from Bambu Studio
	GcodeState         string // IDLE, PREPARE, RUNNING, PAUSE, FINISH, FAILED
	PrintErrorCode     string // Error code if print failed
	RemainingPrintTime int    // Minutes remaining
	PrintPercentDone   int    // Completion percentage (0-100)
}

type Printer struct {
	config *PrinterConfig

	mu         sync.Mutex
	client     paho.Client
	responseCh chan mqttMessage
}

func NewPrinter(config *PrinterConfig) *Printer {
	return &Printer{config: config}
}

// GetState performs a blocking roundtrip to get fresh printer data.
// It connects to the printer if not already connected, requests current state,
// and waits for the response with a timeout.
func (p *Printer) GetState() (PrinterData, error) {
	p.mu.Lock()

	// Connect if needed
	if err := p.ensureConnectedLocked(); err != nil {
		p.mu.Unlock()
		return PrinterData{}, err
	}

	// Set up response channel for this request
	p.responseCh = make(chan mqttMessage, 1)
	p.mu.Unlock()

	// Request fresh data
	if err := p.requestUpdate(); err != nil {
		return PrinterData{}, err
	}

	// Wait for response with timeout
	select {
	case msg := <-p.responseCh:
		p.mu.Lock()
		p.responseCh = nil
		p.mu.Unlock()
		return PrinterData{
			GcodeFile:          msg.Print.GcodeFile,
			SubtaskName:        msg.Print.SubtaskName,
			GcodeState:         msg.Print.GcodeState,
			PrintErrorCode:     msg.Print.McPrintErrorCode,
			RemainingPrintTime: msg.Print.McRemainingTime,
			PrintPercentDone:   msg.Print.McPercent,
		}, nil
	case <-time.After(dataTimeout):
		p.mu.Lock()
		p.responseCh = nil
		p.mu.Unlock()
		return PrinterData{}, fmt.Errorf("timeout waiting for printer data")
	}
}

// ensureConnectedLocked connects to the printer if not already connected.
// Must be called with p.mu held.
func (p *Printer) ensureConnectedLocked() error {
	if p.client != nil && p.client.IsConnected() {
		return nil
	}

	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("ssl://%s:%d", p.config.Host, mqttPort)).
		SetClientID(fmt.Sprintf("conway-%s", p.config.SerialNumber)).
		SetUsername("bblp").
		SetPassword(p.config.AccessCode).
		SetTLSConfig(&tls.Config{InsecureSkipVerify: true}).
		SetAutoReconnect(true).
		SetKeepAlive(30 * time.Second).
		SetConnectTimeout(connectTimeout).
		SetOnConnectHandler(p.onConnect).
		SetConnectionLostHandler(p.onConnectionLost).
		SetDefaultPublishHandler(p.handleMessage)

	p.client = paho.NewClient(opts)

	token := p.client.Connect()
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to connect to printer MQTT: %w", token.Error())
	}

	return nil
}

func (p *Printer) Disconnect() {
	p.mu.Lock()
	if p.responseCh != nil {
		close(p.responseCh)
		p.responseCh = nil
	}
	client := p.client
	p.client = nil
	p.mu.Unlock()

	if client != nil {
		client.Disconnect(250)
	}
}

func (p *Printer) GetSerial() string {
	return p.config.SerialNumber
}

func (p *Printer) StopPrint() error {
	p.mu.Lock()
	if err := p.ensureConnectedLocked(); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("failed to connect: %w", err)
	}
	p.mu.Unlock()

	return p.publishCommand(map[string]any{
		"print": map[string]any{
			"command":     "stop",
			"sequence_id": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
	})
}

func (p *Printer) onConnect(client paho.Client) {
	topic := fmt.Sprintf("device/%s/report", p.config.SerialNumber)
	token := client.Subscribe(topic, mqttQoS, nil)
	if token.Wait() && token.Error() != nil {
		slog.Error("failed to subscribe to printer topic", "error", token.Error(), "serial", p.config.SerialNumber)
		return
	}
	slog.Debug("subscribed to printer MQTT topic", "serial", p.config.SerialNumber)
	if err := p.requestUpdate(); err != nil {
		slog.Warn("failed to request update after connect", "error", err, "serial", p.config.SerialNumber)
	}
}

func (p *Printer) onConnectionLost(client paho.Client, err error) {
	slog.Warn("printer MQTT connection lost", "error", err, "serial", p.config.SerialNumber)
}

func (p *Printer) handleMessage(client paho.Client, msg paho.Message) {
	var received mqttMessage
	if err := json.Unmarshal(msg.Payload(), &received); err != nil {
		slog.Debug("failed to unmarshal printer message", "error", err, "serial", p.config.SerialNumber)
		return
	}

	// Ignore messages that aren't print status responses.
	// Valid responses have a gcode_state field populated.
	if received.Print.GcodeState == "" {
		slog.Debug("ignoring non-status printer message", "serial", p.config.SerialNumber)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.responseCh != nil {
		select {
		case p.responseCh <- received:
		default:
			// Channel full, drop message
		}
	}
}

func (p *Printer) requestUpdate() error {
	return p.publishCommand(map[string]any{
		"pushing": map[string]any{
			"command":     "pushall",
			"sequence_id": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
	})
}

func (p *Printer) publishCommand(cmd map[string]any) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	topic := fmt.Sprintf("device/%s/request", p.config.SerialNumber)
	token := p.client.Publish(topic, mqttQoS, false, data)
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to publish command: %w", token.Error())
	}

	return nil
}

// mqttMessage represents the structure of MQTT messages from Bambu printers.
type mqttMessage struct {
	Print struct {
		GcodeFile        string `json:"gcode_file"`
		SubtaskName      string `json:"subtask_name"` // User-editable plate name
		GcodeState       string `json:"gcode_state"`  // IDLE, PREPARE, RUNNING, PAUSE, FINISH, FAILED
		McPrintErrorCode string `json:"mc_print_error_code"`
		McRemainingTime  int    `json:"mc_remaining_time"` // Minutes
		McPercent        int    `json:"mc_percent"`        // 0-100
	} `json:"print"`
}

// CameraStream returns an io.ReadCloser that provides MJPEG frames from the printer's camera.
// The caller is responsible for closing the reader when done.
// The context is used to terminate the FFmpeg process.
func (p *Printer) CameraStream(ctx context.Context) (io.ReadCloser, error) {
	rtspURL := fmt.Sprintf("rtsps://bblp:%s@%s:322/streaming/live/1", p.config.AccessCode, p.config.Host)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-c:v", "mjpeg",
		"-q:v", "5",
		"-r", "15",
		"-an",
		"-f", "mpjpeg",
		"-boundary_tag", "frame",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create ffmpeg stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	slog.Info("started camera stream", "serial", p.config.SerialNumber)

	return &cmdReader{ReadCloser: stdout, cmd: cmd, serial: p.config.SerialNumber}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	serial string
}

func (c *cmdReader) Close() error {
	err := c.ReadCloser.Close()
	c.cmd.Wait()
	slog.Info("stopped camera stream", "serial", c.serial)
	return err
}
