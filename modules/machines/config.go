package machines

import "github.com/TheLab-ms/conway/engine/config"

// PrinterConfig holds configuration for a single Bambu printer.
type PrinterConfig struct {
	Name         string `json:"name" config:"label=Name,required,placeholder=e.g. Lab Printer 1"`
	Host         string `json:"host" config:"label=Host/IP Address,required,placeholder=e.g. 192.168.1.100"`
	AccessCode   string `json:"access_code" config:"label=Access Code,secret"`
	SerialNumber string `json:"serial_number" config:"label=Serial Number,required,placeholder=e.g. 01P00A123456789"`
}

// Config holds Bambu printer configuration.
type Config struct {
	Printers            []PrinterConfig `json:"printers" config:"label=Printers,item=Printer,key=SerialNumber"`
	PollIntervalSeconds int             `json:"poll_interval_seconds" config:"label=Poll Interval (seconds),section=settings,default=5,min=1,max=60,help=How often to poll printers for status updates. Default: 5 seconds."`
}

// ConfigSpec returns the Bambu configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "bambu",
		Title:       "Bambu",
		Description: configDescription(),
		Type:        Config{},
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "Printers",
				Label:     "Printers",
				ItemLabel: "Printer",
				Help:      "Configure your Bambu printers. Each printer needs a name, host (IP address), access code, and serial number. Find the access code in Bambu Studio: Printer > Device > Local Connection.",
				KeyField:  "SerialNumber",
			},
		},
		Sections: []config.SectionDef{
			{
				Name:  "settings",
				Title: "Polling Settings",
			},
		},
		Order:    30,
		Category: "Integrations",
	}
}
