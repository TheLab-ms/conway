package kiosk

import (
	"encoding/base64"
	"html/template"

	"github.com/TheLab-ms/conway/internal/templates"
	"github.com/TheLab-ms/conway/modules/bootstrap"
)

var (
	offsiteErrorTemplate *template.Template
	kioskTemplate        *template.Template
)

func init() {
	var err error
	offsiteErrorTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/kiosk/templates/offsite_error.html")
	if err != nil {
		panic(err)
	}
	kioskTemplate, err = template.ParseFiles("/home/runner/work/conway/conway/modules/kiosk/templates/kiosk.html")
	if err != nil {
		panic(err)
	}
}

type member struct {
	ID           int64
	AccessStatus string
}

type KioskData struct {
	QrImg string // Base64 encoded image
}

func renderOffsiteError() templates.Component {
	offsiteErrorContent := &templates.TemplateComponent{
		Template: offsiteErrorTemplate,
		Data:     nil,
	}

	return bootstrap.View(offsiteErrorContent)
}

func renderKiosk(qrImg []byte) templates.Component {
	var qrImgStr string
	if qrImg != nil {
		qrImgStr = base64.StdEncoding.EncodeToString(qrImg)
	}

	data := KioskData{
		QrImg: qrImgStr,
	}

	kioskContent := &templates.TemplateComponent{
		Template: kioskTemplate,
		Data:     data,
	}

	return bootstrap.DarkmodeView(kioskContent)
}