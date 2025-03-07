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
}
