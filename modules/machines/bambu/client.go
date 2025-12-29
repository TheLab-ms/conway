package bambu

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	mqttClientID   = "conway-bambu-client"
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
	client paho.Client

	mu         sync.Mutex
	cond       *sync.Cond
	data       mqttMessage
	lastUpdate time.Time
}

func NewPrinter(config *PrinterConfig) *Printer {
	p := &Printer{
		config: config,
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *Printer) Connect() error {
	p.mu.Lock()
	if p.client != nil && p.client.IsConnected() {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("ssl://%s:%d", p.config.Host, mqttPort)).
		SetClientID(mqttClientID).
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
	p.cond.Broadcast()
	p.mu.Unlock()

	if p.client != nil {
		p.client.Disconnect(250)
	}
}

func (p *Printer) GetSerial() string {
	return p.config.SerialNumber
}

// Data blocks until fresh printer data is received, then returns it.
// Returns an error if no data is received within the timeout.
func (p *Printer) Data() (PrinterData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Request fresh data
	p.requestUpdateLocked()

	// Wait for data to arrive (with timeout)
	deadline := time.Now().Add(dataTimeout)
	lastUpdateBefore := p.lastUpdate

	for {
		// Check if we received new data since requesting
		if p.lastUpdate.After(lastUpdateBefore) {
			return PrinterData{
				GcodeFile:          p.data.Print.GcodeFile,
				SubtaskName:        p.data.Print.SubtaskName,
				GcodeState:         p.data.Print.GcodeState,
				PrintErrorCode:     p.data.Print.McPrintErrorCode,
				RemainingPrintTime: p.data.Print.McRemainingTime,
				PrintPercentDone:   p.data.Print.McPercent,
			}, nil
		}

		// Check timeout
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return PrinterData{}, fmt.Errorf("timeout waiting for printer data")
		}

		// Wait for signal with timeout using a goroutine
		done := make(chan struct{})
		go func() {
			time.Sleep(remaining)
			p.mu.Lock()
			p.cond.Broadcast()
			p.mu.Unlock()
			close(done)
		}()

		p.cond.Wait()

		select {
		case <-done:
			// Timer fired, will check timeout on next iteration
		default:
			// Data arrived, will check on next iteration
		}
	}
}

func (p *Printer) StopPrint() error {
	state := p.getGcodeState()
	if state == "IDLE" || state == "" {
		return fmt.Errorf("cannot stop print: printer is %s", state)
	}

	return p.publishCommand(map[string]any{
		"print": map[string]any{
			"command":     "stop",
			"sequence_id": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
	})
}

func (p *Printer) getGcodeState() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data.Print.GcodeState
}

func (p *Printer) onConnect(client paho.Client) {
	topic := fmt.Sprintf("device/%s/report", p.config.SerialNumber)
	token := client.Subscribe(topic, mqttQoS, nil)
	if token.Wait() && token.Error() != nil {
		slog.Error("failed to subscribe to printer topic", "error", token.Error(), "serial", p.config.SerialNumber)
		return
	}
	slog.Debug("subscribed to printer MQTT topic", "serial", p.config.SerialNumber)

	// Request initial data
	p.requestUpdate()
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

	p.mu.Lock()
	p.data = received
	p.lastUpdate = time.Now()
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Printer) requestUpdateLocked() {
	p.mu.Unlock()
	p.requestUpdate()
	p.mu.Lock()
}

func (p *Printer) requestUpdate() {
	err := p.publishCommand(map[string]any{
		"pushing": map[string]any{
			"command":     "pushall",
			"sequence_id": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
	})
	if err != nil {
		slog.Debug("failed to request printer update", "error", err, "serial", p.config.SerialNumber)
	}
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
