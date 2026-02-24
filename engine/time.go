package engine

import (
	"fmt"
	"time"
)

var loc *time.Location

func init() {
	var err error
	loc, err = time.LoadLocation("America/Chicago")
	if err != nil {
		panic(err)
	}
}

type LocalTime struct {
	Time time.Time
}

func (l *LocalTime) Scan(src any) error {
	epochUTC, ok := src.(int64)
	if !ok {
		return fmt.Errorf("expected int64, got %T", src)
	}

	l.Time = time.Unix(epochUTC, 0).In(loc)
	return nil
}

const day = 24 * time.Hour

// FormatTimeAgo returns a human-readable relative time string like "just now",
// "3 minutes ago", "2 hours ago", or "5 days ago". If fallbackAfter is positive
// and the duration exceeds it, the time is formatted using fallbackLayout instead.
// Pass 0 for fallbackAfter to always use relative formatting.
func FormatTimeAgo(ts time.Time, fallbackAfter time.Duration, fallbackLayout string) string {
	dur := time.Since(ts)

	switch {
	case dur < time.Minute:
		return "just now"
	case dur < time.Hour:
		mins := int(dur / time.Minute)
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case dur < day:
		hours := int(dur / time.Hour)
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case fallbackAfter > 0 && dur >= fallbackAfter:
		return ts.Format(fallbackLayout)
	default:
		days := int(dur / day)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
