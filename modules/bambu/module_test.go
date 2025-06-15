package bambu

import (
	"testing"

	"github.com/TheLab-ms/conway/modules/peering"
	"github.com/stretchr/testify/assert"
)

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

func ptrInt64(v int64) *int64 {
	return &v
}
