package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewStreamMux(t *testing.T) {
	called := false
	startSource := func(ctx context.Context) (io.ReadCloser, error) {
		called = true
		return io.NopCloser(bytes.NewReader(nil)), nil
	}

	mux := NewStreamMux(startSource)
	assert.NotNil(t, mux)
	assert.NotNil(t, mux.clients)
	assert.False(t, called, "StartSource should not be called until Subscribe")
}

func TestStreamMux_Subscribe_StartsSource(t *testing.T) {
	started := false
	mux := NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		started = true
		return io.NopCloser(bytes.NewReader([]byte("test"))), nil
	})

	ch := mux.Subscribe()
	assert.NotNil(t, ch)
	assert.True(t, started)
	assert.True(t, mux.Running())
	assert.Equal(t, 1, mux.ClientCount())

	mux.Unsubscribe(ch)
}

func TestStreamMux_Subscribe_SourceError(t *testing.T) {
	mux := NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		return nil, errors.New("source failed")
	})

	ch := mux.Subscribe()
	assert.Nil(t, ch)
	assert.False(t, mux.Running())
	assert.Equal(t, 0, mux.ClientCount())
}

func TestStreamMux_MultipleSubscribers(t *testing.T) {
	startCount := 0
	mux := NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		startCount++
		// Return a reader that blocks until context is canceled
		return &blockingReader{ctx: ctx}, nil
	})

	ch1 := mux.Subscribe()
	ch2 := mux.Subscribe()
	ch3 := mux.Subscribe()

	assert.Equal(t, 1, startCount, "Source should only start once")
	assert.Equal(t, 3, mux.ClientCount())
	assert.True(t, mux.Running())

	mux.Unsubscribe(ch1)
	assert.Equal(t, 2, mux.ClientCount())
	assert.True(t, mux.Running())

	mux.Unsubscribe(ch2)
	assert.Equal(t, 1, mux.ClientCount())
	assert.True(t, mux.Running())

	mux.Unsubscribe(ch3)
	assert.Equal(t, 0, mux.ClientCount())
	assert.False(t, mux.Running())
}

func TestStreamMux_Broadcast(t *testing.T) {
	data := []byte("hello world")
	mux := NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})

	ch := mux.Subscribe()
	assert.NotNil(t, ch)

	// Wait for data with timeout
	select {
	case received := <-ch:
		assert.Equal(t, data, received)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for data")
	}

	// Channel may already be closed by broadcast goroutine on EOF,
	// so we just drain it rather than calling Unsubscribe
	for range ch {
	}
}

func TestStreamMux_BroadcastToMultiple(t *testing.T) {
	data := []byte("broadcast test")
	mux := NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})

	ch1 := mux.Subscribe()
	ch2 := mux.Subscribe()

	// Both should receive the data
	for i, ch := range []chan []byte{ch1, ch2} {
		select {
		case received := <-ch:
			assert.Equal(t, data, received, "channel %d", i)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for data on channel %d", i)
		}
	}

	// Drain channels - they may be closed by broadcast on EOF
	for range ch1 {
	}
	for range ch2 {
	}
}

func TestStreamMux_DefaultSizes(t *testing.T) {
	mux := &StreamMux{}
	assert.Equal(t, 32*1024, mux.bufSize())
	assert.Equal(t, 30, mux.chanSize())
}

func TestStreamMux_CustomSizes(t *testing.T) {
	mux := &StreamMux{
		BufSize:  1024,
		ChanSize: 10,
	}
	assert.Equal(t, 1024, mux.bufSize())
	assert.Equal(t, 10, mux.chanSize())
}

// blockingReader blocks on Read until context is canceled
type blockingReader struct {
	ctx context.Context
}

func (r *blockingReader) Read(p []byte) (n int, err error) {
	<-r.ctx.Done()
	return 0, io.EOF
}

func (r *blockingReader) Close() error {
	return nil
}
