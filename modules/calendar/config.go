package calendar

import (
	"github.com/TheLab-ms/conway/engine/config"
)

// RoomConfig holds configuration for a single room.
type RoomConfig struct {
	Name        string `json:"name" config:"label=Name,required,placeholder=e.g. Woodshop"`
	Description string `json:"description" config:"label=Description,placeholder=Optional description"`
}

// Config holds calendar configuration.
type Config struct {
	Rooms []RoomConfig `json:"rooms" config:"label=Rooms,item=Room,key=Name"`
}

// ConfigSpec returns the calendar configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "calendar",
		Title:       "Calendar",
		Description: `<strong>Room Configuration</strong>
<p class="mb-0 mt-2">Define rooms that can be reserved for calendar events. Each event can be assigned to a specific room, and overlapping events in the same room are prevented.</p>`,
		Type: Config{},
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "Rooms",
				Label:     "Rooms",
				ItemLabel: "Room",
				Help:      "Configure rooms available for event reservations. Events without a room assignment use a shared 'General Area' that also prevents overlaps.",
				KeyField:  "Name",
			},
		},
		Order: 25,
	}
}
