package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPollWorkqueue(t *testing.T) {
	tests := []struct {
		name         string
		items        []string
		getError     error
		processError error
		updateError  error
		returnNil    bool
		expectResult bool
	}{
		{
			name:         "successful processing",
			items:        []string{"item1"},
			expectResult: true,
		},
		{
			name:         "no items available",
			items:        []string{},
			expectResult: false,
		},
		{
			name:         "get next returns no rows",
			items:        []string{},
			getError:     sql.ErrNoRows,
			expectResult: false,
		},
		{
			name:         "get next error",
			getError:     errors.New("db error"),
			expectResult: false,
		},
		{
			name:         "process error marks failed",
			items:        []string{"item1"},
			processError: errors.New("process error"),
			expectResult: true,
		},
		{
			name:         "update error after success",
			items:        []string{"item1"},
			updateError:  errors.New("update error"),
			expectResult: false,
		},
		{
			name:         "update error after failure",
			items:        []string{"item1"},
			processError: errors.New("process error"),
			updateError:  errors.New("update error"),
			expectResult: false,
		},
		{
			name:         "nil item returned",
			returnNil:    true,
			expectResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wq := &mockWorkqueue{
				items:        tt.items,
				getError:     tt.getError,
				processError: tt.processError,
				updateError:  tt.updateError,
				returnNil:    tt.returnNil,
			}

			pollingFunc := PollWorkqueue(wq)
			result := pollingFunc(context.Background())
			assert.Equal(t, tt.expectResult, result)
		})
	}
}

func TestPollWorkqueue_Sequential(t *testing.T) {
	wq := &mockWorkqueue{items: []string{"item1", "item2"}}
	pollingFunc := PollWorkqueue(wq)

	assert.True(t, pollingFunc(t.Context()))
	assert.True(t, pollingFunc(t.Context()))
	assert.False(t, pollingFunc(t.Context()))
}

type mockWorkqueue struct {
	items        []string
	currentIndex int
	getError     error
	processError error
	updateError  error
	returnNil    bool
}

func (m *mockWorkqueue) GetItem(ctx context.Context) (any, error) {
	if m.returnNil {
		return nil, nil
	}
	if m.getError != nil {
		return "", m.getError
	}
	if m.currentIndex >= len(m.items) {
		return "", sql.ErrNoRows
	}
	item := m.items[m.currentIndex]
	m.currentIndex++
	return item, nil
}

func (m *mockWorkqueue) ProcessItem(ctx context.Context, item any) error      { return m.processError }
func (m *mockWorkqueue) UpdateItem(ctx context.Context, i any, ok bool) error { return m.updateError }
