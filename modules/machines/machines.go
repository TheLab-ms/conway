package machines

import (
	"html/template"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	machinesTemplate *template.Template
)

func init() {
	var err error
	machinesTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/machines/templates/machines.html")
	if err != nil {
		panic(err)
	}
}

type printerStatus struct {
	Name              string
	JobFinishedAt     *engine.LocalTime
	ErrorCode         string
	CompletionEstimate string // Pre-computed string
}

type PrinterStatusData struct {
	Name              string
	JobFinishedAt     *engine.LocalTime
	ErrorCode         string
	CompletionEstimate string
}

func renderMachines(printers []*printerStatus) templates.Component {
	// Transform the data to include completion estimates
	data := make([]*PrinterStatusData, len(printers))
	for i, printer := range printers {
		estimate := ""
		if printer.JobFinishedAt != nil {
			estimate = time.Until(printer.JobFinishedAt.Time).Round(time.Minute).String()
		}
		
		data[i] = &PrinterStatusData{
			Name:              printer.Name,
			JobFinishedAt:     printer.JobFinishedAt,
			ErrorCode:         printer.ErrorCode,
			CompletionEstimate: estimate,
		}
	}

	machinesContent := &templates.TemplateComponent{
		Template: machinesTemplate,
		Data:     data,
	}

	return bootstrap.View(machinesContent)
}