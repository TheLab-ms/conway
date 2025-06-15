package bambu

import (
	"sync"
	"time"

	"github.com/TheLab-ms/conway/modules/peering"
	"github.com/google/uuid"
)

type cache struct {
	lock      sync.Mutex
	state     map[string]*peering.Event
	lastFlush time.Time
}

func (c *cache) Add(pe *peering.PrinterEvent) {
	c.lock.Lock()
	defer c.lock.Unlock()

	current, ok := c.state[pe.PrinterName]
	if ok && eventsEqual(current.PrinterEvent, pe) {
		return
	}

	c.state[pe.PrinterName] = &peering.Event{
		UID:          uuid.NewString(),
		Timestamp:    time.Now().Unix(),
		PrinterEvent: pe,
	}
}

func (c *cache) Flush() []*peering.Event {
	c.lock.Lock()
	defer c.lock.Unlock()

	if time.Since(c.lastFlush) < time.Second*10 {
		return nil // only send the events every 10 seconds
	}

	events := []*peering.Event{}
	for _, e := range c.state {
		events = append(events, e)
	}

	c.lastFlush = time.Now()
	return events
}

// eventsEqual returns false if the events represent different error codes or if the job remaining minutes differ by more than 10%.
func eventsEqual(a, b *peering.PrinterEvent) bool {
	if a.PrinterName != b.PrinterName || a.ErrorCode != b.ErrorCode {
		return false
	}
	if a.JobRemainingMinutes == nil || b.JobRemainingMinutes == nil {
		return a.JobRemainingMinutes == nil && b.JobRemainingMinutes == nil
	}

	av, bv := *a.JobRemainingMinutes, *b.JobRemainingMinutes
	if av == bv {
		return true
	}

	diff := av - bv
	if diff < 0 {
		diff = -diff
	}
	threshold := max(av, bv)
	return float64(diff) <= 0.1*float64(threshold)
}
