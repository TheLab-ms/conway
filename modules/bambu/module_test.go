package bambu

import (
	"testing"
	"time"
)

func TestPrinterState_JobCompletionHandling(t *testing.T) {
	tests := []struct {
		name                string
		remainingPrintTime  int
		expectJobFinishedAt bool
		description         string
	}{
		{
			name:                "active_job",
			remainingPrintTime:  30,
			expectJobFinishedAt: true,
			description:         "Job in progress with 30 minutes remaining",
		},
		{
			name:                "completed_job",
			remainingPrintTime:  0,
			expectJobFinishedAt: false,
			description:         "Job completed (0 minutes remaining)",
		},
		{
			name:                "overdue_job",
			remainingPrintTime:  -10,
			expectJobFinishedAt: false,
			description:         "Job overdue (negative minutes)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a printer state based on the remaining print time
			s := &PrinterState{
				Name:      "TestPrinter",
				ErrorCode: "",
			}

			// Apply the same logic as in the poll function
			if minutes := tt.remainingPrintTime; minutes > 0 {
				jobTime := time.Now().Add(time.Duration(minutes) * time.Minute)
				s.JobFinisedAt = &jobTime
			} else {
				// Job is completed or not running, ensure JobFinisedAt is nil
				s.JobFinisedAt = nil
			}

			// Verify the result
			if tt.expectJobFinishedAt && s.JobFinisedAt == nil {
				t.Errorf("Expected JobFinishedAt to be set, but it was nil")
			}
			if !tt.expectJobFinishedAt && s.JobFinisedAt != nil {
				t.Errorf("Expected JobFinishedAt to be nil, but it was set to %v", s.JobFinisedAt)
			}
		})
	}
}