package bambu

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/modules/peering"
	"github.com/stretchr/testify/assert"
)

func TestCache(t *testing.T) {
	printer := "A"
	event := &peering.PrinterEvent{PrinterName: printer, ErrorCode: "1"}
	c := &cache{state: make(map[string]*peering.Event)}

	t.Run("Add stores event", func(t *testing.T) {
		c.Add(event)
		e, ok := c.state[printer]
		assert.True(t, ok)
		assert.Equal(t, event.PrinterName, e.PrinterEvent.PrinterName)
	})

	t.Run("Add deduplicates event", func(t *testing.T) {
		c.Add(event)
		before := c.state[printer].UID
		c.Add(event)
		after := c.state[printer].UID
		assert.Equal(t, before, after)
	})

	t.Run("Flush returns events after 10s", func(t *testing.T) {
		c.lastFlush = time.Now().Add(-11 * time.Second)
		c.Add(event)
		out := c.Flush()
		assert.Len(t, out, 1)
	})

	t.Run("Flush returns nil if called again within 10s", func(t *testing.T) {
		out := c.Flush()
		assert.Nil(t, out)
	})
}

func ptrInt64(v int64) *int64 {
	return &v
}

func TestEventsEqual(t *testing.T) {
	type args struct {
		a *peering.PrinterEvent
		b *peering.PrinterEvent
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "identical names, error codes, nil times",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: nil},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: nil},
			},
			want: true,
		},
		{
			name: "different printer names",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1"},
				b: &peering.PrinterEvent{PrinterName: "B", ErrorCode: "1"},
			},
			want: false,
		},
		{
			name: "different error codes",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1"},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "2"},
			},
			want: false,
		},
		{
			name: "one nil JobRemainingMinutes, one not nil",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: nil},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(10)},
			},
			want: false,
		},
		{
			name: "both JobRemainingMinutes set, equal",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(15)},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(15)},
			},
			want: true,
		},
		{
			name: "JobRemainingMinutes within 10% threshold",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(100)},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(109)},
			},
			want: true,
		},
		{
			name: "JobRemainingMinutes outside 10% threshold",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(100)},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(120)},
			},
			want: false,
		},
		{
			name: "JobRemainingMinutes both zero",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(0)},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(0)},
			},
			want: true,
		},
		{
			name: "JobRemainingMinutes one zero, one nonzero",
			args: args{
				a: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(0)},
				b: &peering.PrinterEvent{PrinterName: "A", ErrorCode: "1", JobRemainingMinutes: ptrInt64(1)},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventsEqual(tt.args.a, tt.args.b)
			assert.Equal(t, tt.want, got)
		})
	}
}
