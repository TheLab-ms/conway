package calendar

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
)

const migration = `
CREATE TABLE IF NOT EXISTS calendar_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    created_by INTEGER REFERENCES members(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    description TEXT,
    start_time INTEGER NOT NULL,
    duration_minutes INTEGER NOT NULL DEFAULT 60,
    recurrence_type TEXT,
    recurrence_day TEXT,
    recurrence_week TEXT,
    recurrence_end INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS calendar_events_start_idx ON calendar_events(start_time);

CREATE TABLE IF NOT EXISTS calendar_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    rooms_json TEXT NOT NULL DEFAULT '[]'
) STRICT;
`

// migrationRoomID adds the room_id column to calendar_events if it doesn't exist.
const migrationRoomID = `
ALTER TABLE calendar_events ADD COLUMN room_id INTEGER;
`

// Event represents a calendar event in the database.
type Event struct {
	ID              int64
	Created         int64
	CreatedBy       *int64
	Title           string
	Description     *string
	StartTime       int64
	DurationMinutes int
	RecurrenceType  *string
	RecurrenceDay   *string
	RecurrenceWeek  *string
	RecurrenceEnd   *int64
	RoomID          *int64
}

type Module struct {
	db           *sql.DB
	self         *url.URL
	nav          []*admin.NavbarTab
	configLoader *config.Loader[Config]
	configStore  *config.Store
}

func New(db *sql.DB, self *url.URL) *Module {
	engine.MustMigrate(db, migration)
	// Add room_id column if it doesn't exist (ignore error if column already exists)
	db.Exec(migrationRoomID)

	return &Module{
		db:   db,
		self: self,
	}
}

// SetConfigRegistry sets the config registry for loading room configuration.
// This should be called after all modules are registered with the App.
func (m *Module) SetConfigRegistry(registry *config.Registry) {
	if registry != nil {
		m.configStore = config.NewStore(m.db, registry)
		m.configLoader = config.NewLoader[Config](m.configStore, "calendar")
	}
}

// SetNavbar sets the admin navigation tabs for the calendar admin pages.
func (m *Module) SetNavbar(nav []*admin.NavbarTab) {
	m.nav = nav
}

func (m *Module) AttachRoutes(router *engine.Router) {
	// Public routes (no auth)
	router.HandleFunc("GET /calendar", m.handleCalendar)
	router.HandleFunc("GET /ical", m.handleICalFeed)

	// Admin routes (leadership required)
	// Using /admin/calendar to avoid conflict with /admin/events (member events audit log)
	router.HandleFunc("GET /admin/calendar", router.WithLeadership(m.handleAdminList))
	router.HandleFunc("GET /admin/calendar/more", router.WithLeadership(m.handleAdminListMore))
	router.HandleFunc("GET /admin/calendar/new", router.WithLeadership(m.handleNewEventForm))
	router.HandleFunc("POST /admin/calendar", router.WithLeadership(m.handleCreateEvent))
	router.HandleFunc("GET /admin/calendar/{id}/edit", router.WithLeadership(m.handleEditEventForm))
	router.HandleFunc("POST /admin/calendar/{id}", router.WithLeadership(m.handleUpdateEvent))
	router.HandleFunc("POST /admin/calendar/{id}/delete", router.WithLeadership(m.handleDeleteEvent))
}

// handleCalendar renders the public calendar page.
func (m *Module) handleCalendar(w http.ResponseWriter, r *http.Request) {
	events, err := m.queryAllEvents(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	// Parse month/year from query params, default to current month
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if y := r.URL.Query().Get("year"); y != "" {
		if parsed, err := strconv.Atoi(y); err == nil && parsed >= 1900 && parsed <= 2100 {
			year = parsed
		}
	}
	if m := r.URL.Query().Get("month"); m != "" {
		if parsed, err := strconv.Atoi(m); err == nil && parsed >= 1 && parsed <= 12 {
			month = parsed
		}
	}

	// Parse day selection
	selectedDay := 0
	if d := r.URL.Query().Get("day"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed >= 1 && parsed <= 31 {
			selectedDay = parsed
		}
	}

	// Expand recurring events for the next 60 days from now
	rangeStart := now
	rangeEnd := now.AddDate(0, 0, 60)
	occurrences := ExpandEvents(events, rangeStart, rangeEnd)

	// Sort by start time
	sort.Slice(occurrences, func(i, j int) bool {
		return occurrences[i].StartTime.Before(occurrences[j].StartTime)
	})

	// Build calendar month data (before filtering so EventDays is complete)
	cal := buildCalendarMonth(occurrences, year, time.Month(month))
	cal.SelectedDay = selectedDay

	// Filter occurrences if day is selected
	if selectedDay > 0 {
		selectedDate := time.Date(year, time.Month(month), selectedDay, 0, 0, 0, 0, time.Local)
		var filtered []EventOccurrence
		for _, occ := range occurrences {
			if occ.StartTime.Year() == selectedDate.Year() &&
				occ.StartTime.Month() == selectedDate.Month() &&
				occ.StartTime.Day() == selectedDate.Day() {
				filtered = append(filtered, occ)
			}
		}
		occurrences = filtered
	}

	w.Header().Set("Content-Type", "text/html")

	// Check if HTMX request - return only the calendar content partial
	if r.Header.Get("HX-Request") == "true" {
		renderCalendarContent(occurrences, cal, rooms).Render(r.Context(), w)
		return
	}

	renderCalendar(occurrences, cal, m.self.Host, rooms).Render(r.Context(), w)
}

// handleICalFeed returns an iCal feed of all events.
func (m *Module) handleICalFeed(w http.ResponseWriter, r *http.Request) {
	events, err := m.queryAllEvents(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=events.ics")

	if err := WriteICalFeed(w, events, m.self.Host, rooms); err != nil {
		engine.HandleError(w, err)
	}
}

// handleAdminList renders the admin event list.
func (m *Module) handleAdminList(w http.ResponseWriter, r *http.Request) {
	const limit = 20

	totalCount, err := m.queryEventCount(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	events, err := m.queryEventsPaginated(r.Context(), limit, 0)
	if engine.HandleError(w, err) {
		return
	}

	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	hasMore := totalCount > limit
	moreURL := ""
	if hasMore {
		moreURL = "/admin/calendar/more?page=2"
	}

	w.Header().Set("Content-Type", "text/html")
	renderAdminList(m.nav, events, hasMore, moreURL, rooms).Render(r.Context(), w)
}

// handleAdminListMore renders additional event rows for infinite scroll.
func (m *Module) handleAdminListMore(w http.ResponseWriter, r *http.Request) {
	const limit = 20

	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 0)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	totalCount, err := m.queryEventCount(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	events, err := m.queryEventsPaginated(r.Context(), limit, int(offset))
	if engine.HandleError(w, err) {
		return
	}

	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	hasMore := totalCount > offset+limit
	moreURL := ""
	if hasMore {
		moreURL = fmt.Sprintf("/admin/calendar/more?page=%d", page+1)
	}

	w.Header().Set("Content-Type", "text/html")
	renderAdminListRows(events, hasMore, moreURL, rooms).Render(r.Context(), w)
}

// handleNewEventForm renders the new event form.
func (m *Module) handleNewEventForm(w http.ResponseWriter, r *http.Request) {
	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderEventForm(m.nav, nil, rooms).Render(r.Context(), w)
}

// handleCreateEvent creates a new event.
func (m *Module) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	event, err := parseEventForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), http.StatusBadRequest)
		return
	}

	// Check for overlapping events
	if err := m.checkEventOverlap(r.Context(), event, 0); err != nil {
		engine.ClientError(w, "Scheduling Conflict", err.Error(), http.StatusConflict)
		return
	}

	userMeta := auth.GetUserMeta(r.Context())
	event.CreatedBy = &userMeta.ID

	_, err = m.db.ExecContext(r.Context(), `
		INSERT INTO calendar_events (
			created_by, title, description, start_time, duration_minutes,
			recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		event.CreatedBy, event.Title, event.Description, event.StartTime, event.DurationMinutes,
		event.RecurrenceType, event.RecurrenceDay, event.RecurrenceWeek, event.RecurrenceEnd, event.RoomID)

	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/calendar", http.StatusSeeOther)
}

// handleEditEventForm renders the edit event form.
func (m *Module) handleEditEventForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "Event ID must be a number", http.StatusBadRequest)
		return
	}

	event, err := m.queryEventByID(r.Context(), id)
	if err == sql.ErrNoRows {
		engine.ClientError(w, "Not Found", "Event not found", http.StatusNotFound)
		return
	}
	if engine.HandleError(w, err) {
		return
	}

	rooms, err := m.loadRooms(r.Context())
	if engine.HandleError(w, err) {
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderEventForm(m.nav, event, rooms).Render(r.Context(), w)
}

// handleUpdateEvent updates an existing event.
func (m *Module) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "Event ID must be a number", http.StatusBadRequest)
		return
	}

	event, err := parseEventForm(r)
	if err != nil {
		engine.ClientError(w, "Invalid Input", err.Error(), http.StatusBadRequest)
		return
	}

	// Check for overlapping events (exclude the event being updated)
	if err := m.checkEventOverlap(r.Context(), event, id); err != nil {
		engine.ClientError(w, "Scheduling Conflict", err.Error(), http.StatusConflict)
		return
	}

	_, err = m.db.ExecContext(r.Context(), `
		UPDATE calendar_events SET
			title = $1, description = $2, start_time = $3, duration_minutes = $4,
			recurrence_type = $5, recurrence_day = $6, recurrence_week = $7, recurrence_end = $8, room_id = $9
		WHERE id = $10`,
		event.Title, event.Description, event.StartTime, event.DurationMinutes,
		event.RecurrenceType, event.RecurrenceDay, event.RecurrenceWeek, event.RecurrenceEnd, event.RoomID, id)

	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/calendar", http.StatusSeeOther)
}

// handleDeleteEvent deletes an event.
func (m *Module) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		engine.ClientError(w, "Invalid ID", "Event ID must be a number", http.StatusBadRequest)
		return
	}

	_, err = m.db.ExecContext(r.Context(), "DELETE FROM calendar_events WHERE id = $1", id)
	if engine.HandleError(w, err) {
		return
	}

	http.Redirect(w, r, "/admin/calendar", http.StatusSeeOther)
}

// queryAllEvents returns all events from the database.
func (m *Module) queryAllEvents(ctx context.Context) ([]*Event, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, created, created_by, title, description, start_time, duration_minutes,
			recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
		FROM calendar_events
		ORDER BY start_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e := &Event{}
		err := rows.Scan(&e.ID, &e.Created, &e.CreatedBy, &e.Title, &e.Description,
			&e.StartTime, &e.DurationMinutes, &e.RecurrenceType, &e.RecurrenceDay,
			&e.RecurrenceWeek, &e.RecurrenceEnd, &e.RoomID)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// queryEventCount returns the total number of events.
func (m *Module) queryEventCount(ctx context.Context) (int64, error) {
	var count int64
	err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM calendar_events").Scan(&count)
	return count, err
}

// queryEventsPaginated returns events with pagination.
func (m *Module) queryEventsPaginated(ctx context.Context, limit, offset int) ([]*Event, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, created, created_by, title, description, start_time, duration_minutes,
			recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
		FROM calendar_events
		ORDER BY start_time
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e := &Event{}
		err := rows.Scan(&e.ID, &e.Created, &e.CreatedBy, &e.Title, &e.Description,
			&e.StartTime, &e.DurationMinutes, &e.RecurrenceType, &e.RecurrenceDay,
			&e.RecurrenceWeek, &e.RecurrenceEnd, &e.RoomID)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// queryEventByID returns a single event by ID.
func (m *Module) queryEventByID(ctx context.Context, id int64) (*Event, error) {
	e := &Event{}
	err := m.db.QueryRowContext(ctx, `
		SELECT id, created, created_by, title, description, start_time, duration_minutes,
			recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
		FROM calendar_events WHERE id = $1`, id).Scan(
		&e.ID, &e.Created, &e.CreatedBy, &e.Title, &e.Description,
		&e.StartTime, &e.DurationMinutes, &e.RecurrenceType, &e.RecurrenceDay,
		&e.RecurrenceWeek, &e.RecurrenceEnd, &e.RoomID)
	return e, err
}

// parseEventForm parses form data into an Event struct.
func parseEventForm(r *http.Request) (*Event, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		return nil, errorf("Title is required")
	}

	description := strings.TrimSpace(r.FormValue("description"))
	var descPtr *string
	if description != "" {
		descPtr = &description
	}

	// Parse date and time
	dateStr := r.FormValue("date")
	timeStr := r.FormValue("time")
	if dateStr == "" || timeStr == "" {
		return nil, errorf("Date and time are required")
	}

	startTime, err := time.ParseInLocation("2006-01-02 15:04", dateStr+" "+timeStr, time.Local)
	if err != nil {
		return nil, errorf("Invalid date/time format")
	}

	// Parse duration
	durationMinutes, err := strconv.Atoi(r.FormValue("duration"))
	if err != nil || durationMinutes < 1 {
		durationMinutes = 60
	}

	// Parse room_id
	var roomID *int64
	if roomStr := r.FormValue("room_id"); roomStr != "" {
		if rid, err := strconv.ParseInt(roomStr, 10, 64); err == nil {
			roomID = &rid
		}
	}

	// Parse recurrence
	var recurrenceType, recurrenceDay, recurrenceWeek *string
	var recurrenceEnd *int64

	recType := strings.ToLower(r.FormValue("recurrence_type"))
	if recType != "" && recType != "none" {
		recurrenceType = &recType

		day := strings.ToLower(r.FormValue("recurrence_day"))
		if day != "" {
			recurrenceDay = &day
		}

		if recType == "monthly" {
			week := strings.ToLower(r.FormValue("recurrence_week"))
			if week != "" {
				recurrenceWeek = &week
			}
		}

		endDateStr := r.FormValue("recurrence_end")
		if endDateStr != "" {
			endDate, err := time.ParseInLocation("2006-01-02", endDateStr, time.Local)
			if err == nil {
				endUnix := endDate.Add(24*time.Hour - time.Second).Unix() // End of day
				recurrenceEnd = &endUnix
			}
		}
	}

	return &Event{
		Title:           title,
		Description:     descPtr,
		StartTime:       startTime.Unix(),
		DurationMinutes: durationMinutes,
		RecurrenceType:  recurrenceType,
		RecurrenceDay:   recurrenceDay,
		RecurrenceWeek:  recurrenceWeek,
		RecurrenceEnd:   recurrenceEnd,
		RoomID:          roomID,
	}, nil
}

type formError string

func (e formError) Error() string { return string(e) }

func errorf(format string, args ...any) error {
	return formError(fmt.Sprintf(format, args...))
}

// loadRooms loads the room configuration.
func (m *Module) loadRooms(ctx context.Context) ([]RoomConfig, error) {
	if m.configLoader == nil {
		return nil, nil
	}
	cfg, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, err
	}
	return cfg.Rooms, nil
}

// queryEventsByRoom returns all events in a specific room (or generic space if roomID is nil).
func (m *Module) queryEventsByRoom(ctx context.Context, roomID *int64, excludeID int64) ([]*Event, error) {
	var rows *sql.Rows
	var err error

	if roomID == nil {
		rows, err = m.db.QueryContext(ctx, `
			SELECT id, created, created_by, title, description, start_time, duration_minutes,
				recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
			FROM calendar_events
			WHERE room_id IS NULL AND id != $1
			ORDER BY start_time`, excludeID)
	} else {
		rows, err = m.db.QueryContext(ctx, `
			SELECT id, created, created_by, title, description, start_time, duration_minutes,
				recurrence_type, recurrence_day, recurrence_week, recurrence_end, room_id
			FROM calendar_events
			WHERE room_id = $1 AND id != $2
			ORDER BY start_time`, *roomID, excludeID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e := &Event{}
		err := rows.Scan(&e.ID, &e.Created, &e.CreatedBy, &e.Title, &e.Description,
			&e.StartTime, &e.DurationMinutes, &e.RecurrenceType, &e.RecurrenceDay,
			&e.RecurrenceWeek, &e.RecurrenceEnd, &e.RoomID)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// countEventsByRoomID returns the number of events that use a specific room ID.
func (m *Module) countEventsByRoomID(ctx context.Context, roomID int64) (int, error) {
	var count int
	err := m.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM calendar_events WHERE room_id = $1`, roomID).Scan(&count)
	return count, err
}

// getRoomName returns the room name for a given room ID, or empty string if not found.
func (m *Module) getRoomName(ctx context.Context, roomID *int64) string {
	if roomID == nil {
		return ""
	}
	rooms, err := m.loadRooms(ctx)
	if err != nil {
		return ""
	}
	for i, r := range rooms {
		if int64(i) == *roomID {
			return r.Name
		}
	}
	return ""
}
