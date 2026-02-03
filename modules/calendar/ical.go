package calendar

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// WriteICalFeed writes an iCal feed for the given events to the writer.
func WriteICalFeed(w io.Writer, events []*Event, hostname string, rooms []RoomConfig) error {
	// Write calendar header
	fmt.Fprintln(w, "BEGIN:VCALENDAR")
	fmt.Fprintln(w, "VERSION:2.0")
	fmt.Fprintln(w, "PRODID:-//Conway//Events//EN")
	fmt.Fprintln(w, "CALSCALE:GREGORIAN")
	fmt.Fprintln(w, "METHOD:PUBLISH")
	fmt.Fprintf(w, "X-WR-CALNAME:%s Events\n", hostname)

	for _, e := range events {
		if err := writeVEvent(w, e, hostname, rooms); err != nil {
			return err
		}
	}

	fmt.Fprintln(w, "END:VCALENDAR")
	return nil
}

// writeVEvent writes a single VEVENT to the writer.
func writeVEvent(w io.Writer, e *Event, hostname string, rooms []RoomConfig) error {
	fmt.Fprintln(w, "BEGIN:VEVENT")

	// UID must be unique and stable
	fmt.Fprintf(w, "UID:event-%d@%s\n", e.ID, hostname)

	// Timestamps
	startTime := time.Unix(e.StartTime, 0).UTC()
	fmt.Fprintf(w, "DTSTART:%s\n", formatICalDateTime(startTime))

	// Duration
	fmt.Fprintf(w, "DURATION:PT%dM\n", e.DurationMinutes)

	// Created/modified timestamp
	created := time.Unix(e.Created, 0).UTC()
	fmt.Fprintf(w, "DTSTAMP:%s\n", formatICalDateTime(created))
	fmt.Fprintf(w, "CREATED:%s\n", formatICalDateTime(created))

	// Summary (title)
	fmt.Fprintf(w, "SUMMARY:%s\n", escapeICalText(e.Title))

	// Description
	if e.Description != nil && *e.Description != "" {
		fmt.Fprintf(w, "DESCRIPTION:%s\n", escapeICalText(*e.Description))
	}

	// Location (room)
	if e.RoomID != nil && int(*e.RoomID) < len(rooms) {
		fmt.Fprintf(w, "LOCATION:%s\n", escapeICalText(rooms[*e.RoomID].Name))
	}

	// Recurrence rule
	if e.RecurrenceType != nil && *e.RecurrenceType != "" {
		rrule := buildRRule(e)
		if rrule != "" {
			fmt.Fprintf(w, "RRULE:%s\n", rrule)
		}
	}

	fmt.Fprintln(w, "END:VEVENT")
	return nil
}

// buildRRule constructs an iCal RRULE string from the event's recurrence fields.
func buildRRule(e *Event) string {
	if e.RecurrenceType == nil || *e.RecurrenceType == "" || e.RecurrenceDay == nil {
		return ""
	}

	recType := strings.ToLower(*e.RecurrenceType)
	dayAbbr := dayToICalAbbr(*e.RecurrenceDay)

	var parts []string

	switch recType {
	case "weekly":
		parts = append(parts, "FREQ=WEEKLY")
		parts = append(parts, "BYDAY="+dayAbbr)

	case "monthly":
		parts = append(parts, "FREQ=MONTHLY")
		if e.RecurrenceWeek != nil {
			week := strings.ToLower(*e.RecurrenceWeek)
			n := weekToICalN(week)
			parts = append(parts, fmt.Sprintf("BYDAY=%s%s", n, dayAbbr))
		} else {
			parts = append(parts, "BYDAY=1"+dayAbbr)
		}
	}

	// Add UNTIL if there's an end date
	if e.RecurrenceEnd != nil && *e.RecurrenceEnd > 0 {
		endTime := time.Unix(*e.RecurrenceEnd, 0).UTC()
		parts = append(parts, "UNTIL="+formatICalDateTime(endTime))
	}

	return strings.Join(parts, ";")
}

// dayToICalAbbr converts a day name to iCal two-letter abbreviation.
func dayToICalAbbr(day string) string {
	abbrs := map[string]string{
		"sunday":    "SU",
		"monday":    "MO",
		"tuesday":   "TU",
		"wednesday": "WE",
		"thursday":  "TH",
		"friday":    "FR",
		"saturday":  "SA",
	}
	if abbr, ok := abbrs[strings.ToLower(day)]; ok {
		return abbr
	}
	return "MO"
}

// weekToICalN converts a week position to iCal format.
func weekToICalN(week string) string {
	switch strings.ToLower(week) {
	case "first":
		return "1"
	case "second":
		return "2"
	case "third":
		return "3"
	case "fourth":
		return "4"
	case "last":
		return "-1"
	default:
		return "1"
	}
}

// formatICalDateTime formats a time in iCal format (YYYYMMDDTHHMMSSZ).
func formatICalDateTime(t time.Time) string {
	return t.Format("20060102T150405Z")
}

// escapeICalText escapes special characters in iCal text fields.
func escapeICalText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
