package camera

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
)

var ErrNotConnected = errors.New("camera: not connected")

// ClientConfig holds the configuration for the CameraClient
type ClientConfig struct {
	Hostname   string
	AccessCode string
	Username   string
	Port       int
}

// Client represents a client to interact with the camera
type Client struct {
	hostname    string
	port        int
	username    string
	authPacket  []byte
	conn        *tls.Conn
	connMutex   sync.Mutex
	streaming   bool
	streamMutex sync.Mutex
	streamChan  chan []byte
	stopChan    chan struct{}
}

// NewClient creates a new CameraClient with the given configuration
func NewClient(config *ClientConfig) *Client {
	if config.Port == 0 {
		config.Port = 6000
	}
	client := &Client{
		hostname:   config.Hostname,
		port:       config.Port,
		username:   config.Username,
		authPacket: createAuthPacket(config.Username, config.AccessCode),
		streamChan: make(chan []byte),
		stopChan:   make(chan struct{}),
	}
	return client
}

// createAuthPacket creates an authentication packet for the camera
func createAuthPacket(username string, accessCode string) []byte {
	authData := make([]byte, 0)
	authData = append(authData, make([]byte, 4)...)
	binary.LittleEndian.PutUint32(authData[0:], 0x40) // '@'\0\0\0
	authData = append(authData, make([]byte, 4)...)
	binary.LittleEndian.PutUint32(authData[4:], 0x3000) // \0'0'\0\0
	authData = append(authData, make([]byte, 8)...)

	authData = append(authData, []byte(username)...)
	authData = append(authData, make([]byte, 32-len(username))...)
	authData = append(authData, []byte(accessCode)...)
	authData = append(authData, make([]byte, 32-len(accessCode))...)
	return authData
}

// findJPEG finds a JPEG image in the buffer and returns the image and the remaining buffer
func (c *Client) findJPEG(buf []byte, startMarker []byte, endMarker []byte) ([]byte, []byte) {
	start := indexOf(buf, startMarker)
	end := indexOf(buf, endMarker, start+len(startMarker))
	if start != -1 && end != -1 {
		return buf[start : end+len(endMarker)], buf[end+len(endMarker):]
	}
	return nil, buf
}

// indexOf finds the index of a subarray in a buffer starting from a given index
func indexOf(buf []byte, sub []byte, start ...int) int {
	s := 0
	if len(start) > 0 {
		s = start[0]
	}
	for i := s; i <= len(buf)-len(sub); i++ {
		if string(buf[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}

// Connect establishes a TLS connection to the camera and sends the authentication packet
func (c *Client) Connect() error {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	if c.conn != nil {
		return nil // Already connected
	}

	config := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%d", c.hostname, c.port), config)
	if err != nil {
		return fmt.Errorf("error connecting to camera: %w", err)
	}

	_, err = conn.Write(c.authPacket)
	if err != nil {
		conn.Close()
		return fmt.Errorf("error sending auth packet: %w", err)
	}

	c.conn = conn

	return nil
}

// Disconnect closes the connection to the camera
func (c *Client) Disconnect() error {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	if c.conn == nil {
		return nil // Already disconnected
	}

	err := c.conn.Close()
	c.conn = nil
	return err
}

// CaptureFrame captures a single frame from the camera
func (c *Client) CaptureFrame() ([]byte, error) {
	if c.conn == nil {
		return nil, ErrNotConnected
	}

	buf := make([]byte, 0)
	readChunkSize := 4096
	jpegStart := []byte{0xff, 0xd8, 0xff, 0xe0}
	jpegEnd := []byte{0xff, 0xd9}

	for {
		dr := make([]byte, readChunkSize)
		n, err := c.conn.Read(dr)
		if err != nil {
			break
		}
		buf = append(buf, dr[:n]...)
		img, remaining := c.findJPEG(buf, jpegStart, jpegEnd)
		if img != nil {
			return img, nil
		}
		buf = remaining
	}
	return nil, nil
}

// readStream reads the stream from the camera
func (c *Client) readStream() error {
	if c.conn == nil {
		return ErrNotConnected
	}

	buf := make([]byte, 0, 4096)
	readChunkSize := 4096
	jpegStart := []byte{0xff, 0xd8, 0xff, 0xe0}
	jpegEnd := []byte{0xff, 0xd9}

	for c.streaming {
		select {
		case <-c.stopChan:
			return nil
		default:
			dr := make([]byte, readChunkSize)
			n, err := c.conn.Read(dr)
			if err != nil {
				if err != io.EOF {
					return fmt.Errorf("error reading stream: %w", err)
				}
				return nil
			}
			buf = append(buf, dr[:n]...)
			for {
				img, remaining := c.findJPEG(buf, jpegStart, jpegEnd)
				if img == nil {
					buf = remaining
					break
				}
				c.streamChan <- img
				buf = remaining
			}
		}
	}
	return nil
}

// captureStream continuously reads from the camera's stream
func (c *Client) captureStream() {
	for c.streaming {
		err := c.readStream()
		if err != nil {
			log.Printf("Error during streaming: %v", err)
			select {
			case <-c.stopChan:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// StartStream starts the video stream from the camera
func (c *Client) StartStream() (<-chan []byte, error) {
	c.streamMutex.Lock()
	defer c.streamMutex.Unlock()
	if c.streaming {
		return nil, fmt.Errorf("stream already running")
	}

	c.streaming = true
	go c.captureStream()
	return c.streamChan, nil
}

// StopStream stops the video stream from the camera
func (c *Client) StopStream() error {
	c.streamMutex.Lock()
	defer c.streamMutex.Unlock()
	if !c.streaming {
		return fmt.Errorf("stream is not running")
	}

	c.streaming = false
	close(c.stopChan)
	c.stopChan = make(chan struct{})
	return nil
}
