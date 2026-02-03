package calendar

import (
	"context"
	"time"
)

// checkEventOverlap checks if the given event would overlap with any existing event
// in the same room. If excludeID is non-zero, that event is excluded from the check
// (used when updating an existing event).
func (m *Module) checkEventOverlap(ctx context.Context, event *Event, excludeID int64) error {
	// Get all events in the same room (or generic space)
	existingEvents, err := m.queryEventsByRoom(ctx, event.RoomID, excludeID)
	if err != nil {
		return err
	}

	if len(existingEvents) == 0 {
		return nil
	}

	// Define the overlap check window: from now to 1 year ahead
	now := time.Now()
	rangeStart := now.Add(-24 * time.Hour) // Include events from yesterday to catch ongoing events
	rangeEnd := now.AddDate(1, 0, 0)       // 1 year ahead

	// Expand the new event to occurrences
	newOccurrences := ExpandEvents([]*Event{event}, rangeStart, rangeEnd)
	if len(newOccurrences) == 0 {
		return nil
	}

	// Expand existing events to occurrences
	existingOccurrences := ExpandEvents(existingEvents, rangeStart, rangeEnd)

	// Check for overlaps between any pair of occurrences
	for _, newOcc := range newOccurrences {
		for _, existingOcc := range existingOccurrences {
			if timesOverlap(newOcc.StartTime, newOcc.EndTime, existingOcc.StartTime, existingOcc.EndTime) {
				var roomSuffix string
				if event.RoomID != nil {
					if name := m.getRoomName(ctx, event.RoomID); name != "" {
						roomSuffix = " in " + name
					}
				}
				return errorf(
					"This event overlaps with \"%s\" on %s%s",
					existingOcc.Event.Title,
					existingOcc.StartTime.Format("Mon, Jan 2 at 3:04 PM"),
					roomSuffix,
				)
			}
		}
	}

	return nil
}

// timesOverlap returns true if two time ranges overlap.
// Two ranges [s1, e1) and [s2, e2) overlap if s1 < e2 AND s2 < e1.
func timesOverlap(start1, end1, start2, end2 time.Time) bool {
	return start1.Before(end2) && start2.Before(end1)
}
