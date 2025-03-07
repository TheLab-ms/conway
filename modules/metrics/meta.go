package metrics

import "time"

type aggregate struct {
	Name     string
	Query    string
	Interval time.Duration
}

var aggregates = []*aggregate{
	{
		Name:     "active-members",
		Query:    "SELECT COUNT(*) FROM members WHERE access_status = 'Ready'",
		Interval: 24 * time.Hour,
	},
	{
		Name:     "daily-unique-visitors",
		Query:    "SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last",
		Interval: 24 * time.Hour,
	},
	{
		Name:     "weekly-unique-visitors",
		Query:    "SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last",
		Interval: 7 * 24 * time.Hour,
	},
	{
		Name:     "monthly-unique-visitors",
		Query:    "SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last",
		Interval: 30 * 24 * time.Hour,
	},
}
