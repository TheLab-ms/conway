package engine

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// StreamMux multiplexes a single data source to multiple subscribers.
// It lazily starts the source when the first subscriber connects and
// automatically stops it when the last subscriber disconnects.
type StreamMux struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	running bool
	cancel  context.CancelFunc
	gen     uint64 // generation counter to track broadcast instances

	// source is called when the first client subscribes.
	// It should return an io.ReadCloser that provides the stream data.
	// The context will be canceled when all clients disconnect.
	source func(ctx context.Context) (io.ReadCloser, error)
}

func NewStreamMux(source func(ctx context.Context) (io.ReadCloser, error)) *StreamMux {
	return &StreamMux{
		clients: make(map[chan []byte]struct{}),
		source:  source,
	}
}

// Subscribe returns a channel that receives stream data.
// The caller must call Unsubscribe when done to avoid resource leaks.
// If the source fails to start, returns nil.
func (s *StreamMux) Subscribe() chan []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Start source if this is the first client
	if !s.running {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.gen++
		myGen := s.gen

		reader, err := s.source(ctx)
		if err != nil {
			slog.Error("streammux: failed to start source", "error", err)
			cancel()
			return nil
		}

		s.running = true
		go s.broadcast(ctx, reader, myGen)
	}

	ch := make(chan []byte, 30)
	s.clients[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a client from the stream.
// When the last client unsubscribes, the source is stopped.
func (s *StreamMux) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.clients, ch)
	close(ch)

	// Stop source if no clients remain
	if len(s.clients) == 0 && s.cancel != nil {
		s.cancel()
		s.running = false
		s.cancel = nil
	}
}

func (s *StreamMux) broadcast(ctx context.Context, reader io.ReadCloser, myGen uint64) {
	defer reader.Close()
	defer func() {
		s.mu.Lock()
		// Only clean up if we're still the active broadcast (generation matches)
		if s.gen == myGen {
			s.running = false
			// Close all remaining client channels
			for ch := range s.clients {
				close(ch)
				delete(s.clients, ch)
			}
		}
		s.mu.Unlock()
	}()

	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := reader.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			s.mu.RLock()
			for ch := range s.clients {
				select {
				case ch <- data:
				default:
					// Drop frame for slow client
				}
			}
			s.mu.RUnlock()
		}
		if err != nil {
			if err != io.EOF {
				slog.Error("streammux: read error", "error", err)
			}
			return
		}
	}
}

// ClientCount returns the current number of subscribers.
func (s *StreamMux) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// Running returns whether the source is currently active.
func (s *StreamMux) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}
