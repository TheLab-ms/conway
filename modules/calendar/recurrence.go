package calendar

import (
	"strings"
	"time"
)

// Weekday maps day names to time.Weekday values.
var dayNameToWeekday = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// weekNameToN maps week position names to their ordinal values.
var weekNameToN = map[string]int{
	"first":  1,
	"second": 2,
	"third":  3,
	"fourth": 4,
	// "last" is handled specially
}

// EventOccurrence represents a single occurrence of an event (either one-time or expanded from recurring).
type EventOccurrence struct {
	Event     *Event
	StartTime time.Time
	EndTime   time.Time
}

// ExpandEvents takes a list of events and expands any recurring events into
// individual occurrences within the given time range. One-time events within
// the range are included as-is.
func ExpandEvents(events []*Event, rangeStart, rangeEnd time.Time) []EventOccurrence {
	var occurrences []EventOccurrence

	for _, e := range events {
		if e.RecurrenceType == nil || *e.RecurrenceType == "" {
			// One-time event
			eventStart := time.Unix(e.StartTime, 0)
			eventEnd := eventStart.Add(time.Duration(e.DurationMinutes) * time.Minute)

			if eventStart.Before(rangeEnd) && eventEnd.After(rangeStart) {
				occurrences = append(occurrences, EventOccurrence{
					Event:     e,
					StartTime: eventStart,
					EndTime:   eventEnd,
				})
			}
		} else {
			// Recurring event
			occs := expandRecurring(e, rangeStart, rangeEnd)
			occurrences = append(occurrences, occs...)
		}
	}

	return occurrences
}

// expandRecurring expands a recurring event into individual occurrences.
func expandRecurring(e *Event, rangeStart, rangeEnd time.Time) []EventOccurrence {
	if e.RecurrenceType == nil || e.RecurrenceDay == nil {
		return nil
	}

	recurrenceType := strings.ToLower(*e.RecurrenceType)
	dayName := strings.ToLower(*e.RecurrenceDay)

	weekday, ok := dayNameToWeekday[dayName]
	if !ok {
		return nil
	}

	// Determine the end of recurrence (either explicit or range end)
	recEnd := rangeEnd
	if e.RecurrenceEnd != nil && *e.RecurrenceEnd > 0 {
		explicitEnd := time.Unix(*e.RecurrenceEnd, 0)
		if explicitEnd.Before(recEnd) {
			recEnd = explicitEnd
		}
	}

	eventStart := time.Unix(e.StartTime, 0)
	duration := time.Duration(e.DurationMinutes) * time.Minute

	var occurrences []EventOccurrence

	switch recurrenceType {
	case "weekly":
		occurrences = expandWeekly(e, eventStart, duration, weekday, rangeStart, recEnd)
	case "monthly":
		week := ""
		if e.RecurrenceWeek != nil {
			week = strings.ToLower(*e.RecurrenceWeek)
		}
		occurrences = expandMonthly(e, eventStart, duration, weekday, week, rangeStart, recEnd)
	}

	return occurrences
}

// expandWeekly generates weekly occurrences.
func expandWeekly(e *Event, eventStart time.Time, duration time.Duration, weekday time.Weekday, rangeStart, rangeEnd time.Time) []EventOccurrence {
	var occurrences []EventOccurrence

	// Find the first occurrence on or after the event's start date
	current := eventStart
	for current.Weekday() != weekday {
		current = current.AddDate(0, 0, 1)
	}
	// Preserve the time from the original event
	current = time.Date(current.Year(), current.Month(), current.Day(),
		eventStart.Hour(), eventStart.Minute(), eventStart.Second(), 0, eventStart.Location())

	// Generate occurrences
	for current.Before(rangeEnd) {
		if !current.Before(rangeStart) {
			occurrences = append(occurrences, EventOccurrence{
				Event:     e,
				StartTime: current,
				EndTime:   current.Add(duration),
			})
		}
		current = current.AddDate(0, 0, 7)
	}

	return occurrences
}

// expandMonthly generates monthly occurrences for patterns like "first Tuesday" or "last Friday".
func expandMonthly(e *Event, eventStart time.Time, duration time.Duration, weekday time.Weekday, week string, rangeStart, rangeEnd time.Time) []EventOccurrence {
	var occurrences []EventOccurrence

	// Start from the month of the event or range start, whichever is later
	startMonth := eventStart
	if rangeStart.After(eventStart) {
		startMonth = rangeStart
	}

	// Iterate through months
	year, month := startMonth.Year(), startMonth.Month()
	for {
		var occurrence time.Time
		if week == "last" {
			occurrence = lastWeekdayOfMonth(year, month, weekday, eventStart.Hour(), eventStart.Minute(), eventStart.Location())
		} else {
			n, ok := weekNameToN[week]
			if !ok {
				n = 1 // default to first
			}
			occurrence = nthWeekdayOfMonth(year, month, weekday, n, eventStart.Hour(), eventStart.Minute(), eventStart.Location())
		}

		if occurrence.IsZero() {
			// Skip invalid dates (e.g., 5th Tuesday doesn't exist in all months)
			month++
			if month > 12 {
				month = 1
				year++
			}
			continue
		}

		if !occurrence.Before(rangeEnd) {
			break
		}

		if !occurrence.Before(rangeStart) && !occurrence.Before(eventStart) {
			occurrences = append(occurrences, EventOccurrence{
				Event:     e,
				StartTime: occurrence,
				EndTime:   occurrence.Add(duration),
			})
		}

		// Move to next month
		month++
		if month > 12 {
			month = 1
			year++
		}
	}

	return occurrences
}

// nthWeekdayOfMonth returns the nth occurrence of a weekday in the given month.
// For example, nthWeekdayOfMonth(2026, 2, time.Tuesday, 1, 19, 0, loc) returns the first Tuesday of Feb 2026.
func nthWeekdayOfMonth(year int, month time.Month, weekday time.Weekday, n int, hour, minute int, loc *time.Location) time.Time {
	if n < 1 || n > 5 {
		return time.Time{}
	}

	// Start at the first of the month
	first := time.Date(year, month, 1, hour, minute, 0, 0, loc)

	// Find the first occurrence of the weekday
	daysUntil := int(weekday - first.Weekday())
	if daysUntil < 0 {
		daysUntil += 7
	}
	firstOccurrence := first.AddDate(0, 0, daysUntil)

	// Add weeks to get the nth occurrence
	result := firstOccurrence.AddDate(0, 0, (n-1)*7)

	// Verify we're still in the same month
	if result.Month() != month {
		return time.Time{}
	}

	return result
}

// lastWeekdayOfMonth returns the last occurrence of a weekday in the given month.
func lastWeekdayOfMonth(year int, month time.Month, weekday time.Weekday, hour, minute int, loc *time.Location) time.Time {
	// Start at the last day of the month
	nextMonth := month + 1
	nextYear := year
	if nextMonth > 12 {
		nextMonth = 1
		nextYear++
	}
	lastDay := time.Date(nextYear, nextMonth, 1, hour, minute, 0, 0, loc).AddDate(0, 0, -1)

	// Find the last occurrence of the weekday
	daysBack := int(lastDay.Weekday() - weekday)
	if daysBack < 0 {
		daysBack += 7
	}
	return lastDay.AddDate(0, 0, -daysBack)
}

// FormatRecurrence returns a human-readable description of the recurrence pattern.
func FormatRecurrence(e *Event) string {
	if e.RecurrenceType == nil || *e.RecurrenceType == "" {
		return "One-time"
	}

	recType := strings.ToLower(*e.RecurrenceType)
	day := ""
	if e.RecurrenceDay != nil {
		day = titleCase(strings.ToLower(*e.RecurrenceDay))
	}

	switch recType {
	case "weekly":
		return "Every " + day
	case "monthly":
		week := "first"
		if e.RecurrenceWeek != nil {
			week = strings.ToLower(*e.RecurrenceWeek)
		}
		return titleCase(week) + " " + day + " of month"
	default:
		return "Unknown"
	}
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
