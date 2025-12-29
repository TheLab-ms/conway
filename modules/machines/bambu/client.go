// Package bambu provides a minimal MQTT client for Bambu Lab printers.
// This client exposes only the fields needed by the machines module,
// including SubtaskName (plate name) which is user-editable in Bambu Studio.
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
	updateInterval = 10 * time.Second
	connectTimeout = 5 * time.Second
)

// PrinterConfig holds the configuration for connecting to a Bambu printer.
type PrinterConfig struct {
	Host         string
	AccessCode   string
	SerialNumber string
}

// PrinterData contains the relevant printer status fields.
// This is a minimal struct exposing only what we need.
type PrinterData struct {
	GcodeFile          string // Current gcode filename
	SubtaskName        string // User-editable plate name from Bambu Studio
	GcodeState         string // IDLE, PREPARE, RUNNING, PAUSE, FINISH, FAILED
	PrintErrorCode     string // Error code if print failed
	RemainingPrintTime int    // Minutes remaining
	PrintPercentDone   int    // Completion percentage (0-100)

	// UI-related fields populated by the machines module
	PrinterName          string `json:"printer_name"`
	SerialNumber         string `json:"serial_number"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
	GcodeFileDisplay     string `json:"gcode_file_display"` // Display name (SubtaskName if set, else stripped gcode filename)
	DiscordUserID        string `json:"discord_user_id"`
	OwnerDiscordUsername string `json:"owner_discord_username"`
}

// IsEmpty returns true if no meaningful data has been received.
func (d *PrinterData) IsEmpty() bool {
	return d.GcodeFile == "" && d.SubtaskName == "" && d.GcodeState == ""
}

// Printer represents a connection to a Bambu Lab printer.
type Printer struct {
	config *PrinterConfig
	client paho.Client

	mu         sync.RWMutex
	data       mqttMessage
	lastUpdate time.Time

	stopChan chan struct{}
	stopped  bool
}

// NewPrinter creates a new Printer instance.
func NewPrinter(config *PrinterConfig) *Printer {
	return &Printer{
		config:   config,
		stopChan: make(chan struct{}),
	}
}

// Connect establishes an MQTT connection to the printer.
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

	// Start periodic update goroutine
	go p.periodicUpdate()

	return nil
}

// Disconnect closes the MQTT connection.
func (p *Printer) Disconnect() {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.stopChan)
	}
	p.mu.Unlock()

	if p.client != nil {
		p.client.Disconnect(250)
	}
}

// GetSerial returns the printer's serial number.
func (p *Printer) GetSerial() string {
	return p.config.SerialNumber
}

// Data returns the current printer data.
func (p *Printer) Data() (PrinterData, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Request update if data is stale
	if time.Since(p.lastUpdate) > connectTimeout {
		go p.requestUpdate()
	}

	return PrinterData{
		GcodeFile:          p.data.Print.GcodeFile,
		SubtaskName:        p.data.Print.SubtaskName,
		GcodeState:         p.data.Print.GcodeState,
		PrintErrorCode:     p.data.Print.McPrintErrorCode,
		RemainingPrintTime: p.data.Print.McRemainingTime,
		PrintPercentDone:   p.data.Print.McPercent,
	}, nil
}

// StopPrint sends a stop command to the printer.
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
	p.mu.RLock()
	defer p.mu.RUnlock()
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
	defer p.mu.Unlock()

	// Merge non-zero fields from received into p.data
	p.mergeData(&received)
	p.lastUpdate = time.Now()
}

func (p *Printer) mergeData(received *mqttMessage) {
	// Only update fields that have non-zero values in the received message
	if received.Print.GcodeFile != "" {
		p.data.Print.GcodeFile = received.Print.GcodeFile
	}
	if received.Print.SubtaskName != "" {
		p.data.Print.SubtaskName = received.Print.SubtaskName
	}
	if received.Print.GcodeState != "" {
		p.data.Print.GcodeState = received.Print.GcodeState
	}
	if received.Print.McPrintErrorCode != "" {
		p.data.Print.McPrintErrorCode = received.Print.McPrintErrorCode
	}
	if received.Print.McRemainingTime != 0 {
		p.data.Print.McRemainingTime = received.Print.McRemainingTime
	}
	if received.Print.McPercent != 0 {
		p.data.Print.McPercent = received.Print.McPercent
	}
}

func (p *Printer) periodicUpdate() {
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.requestUpdate()
		case <-p.stopChan:
			return
		}
	}
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
// This is a minimal struct containing only the fields we need.
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
