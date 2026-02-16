package triggers

import "github.com/TheLab-ms/conway/engine/config"

// TriggerItem represents a single trigger in the config array.
type TriggerItem struct {
	Name         string `json:"name" config:"label=Name,placeholder=e.g. Log: Email confirmed,required"`
	TriggerTable string `json:"trigger_table" config:"label=Table,options=members|member_events|fob_swipes|waivers|discord_webhook_queue,placeholder=Select table"`
	TriggerOp    string `json:"trigger_op" config:"label=Operation,options=INSERT|UPDATE|DELETE"`
	WhenClause   string `json:"when_clause" config:"label=WHEN Condition,placeholder=e.g. OLD.confirmed = 0 AND NEW.confirmed = 1,help=SQL expression for the trigger's WHEN clause. Use <code>NEW.column</code> for INSERT/UPDATE or <code>OLD.column</code> for DELETE. Leave empty to fire on every matching operation."`
	ActionSQL    string `json:"action_sql" config:"label=Action SQL,multiline,rows=4,required,placeholder=INSERT INTO member_events (member\\, event\\, details) VALUES (NEW.id\\, 'MyEvent'\\, 'Something happened');,help=The SQL statement(s) to execute when the trigger fires. Reference row data with <code>NEW.column</code> (INSERT/UPDATE) or <code>OLD.column</code> (DELETE)."`
	Enabled      bool   `json:"enabled" config:"label=Enabled"`
}

// Config holds the triggers configuration.
type Config struct {
	Triggers []TriggerItem `json:"triggers" config:"label=SQL Triggers,item=Trigger,key=Name"`
}

// ConfigSpec returns the config specification for the triggers page.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "triggers",
		Title:       "SQL Triggers",
		Description: triggersDescription(),
		Type:        Config{},
		DevPage:     true,
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "Triggers",
				Label:     "SQL Triggers",
				ItemLabel: "Trigger",
				Help:      "Each trigger fires a SQL statement when a specified database operation occurs on a table. Triggers execute SQL statements automatically when database changes occur. Use them to log member events, send Discord notifications, or perform any custom SQL action.",
				KeyField:  "Name",
			},
		},
		Order: 5,
	}
}
