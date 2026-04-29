package signs

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	ipp "github.com/phin1x/go-ipp"
)

// PrintJob represents a single document to send to a printer.
type PrintJob struct {
	JobName string // Human-readable job name shown in printer queues.
	PDF     []byte // The PDF bytes to print.
}

// Printer is an abstraction over a network print target. Used by the queue
// worker to deliver rendered sign PDFs. Tests inject a fake.
type Printer interface {
	Print(ctx context.Context, job PrintJob) error
}

// PrinterTarget describes how to reach a network printer over IPP.
type PrinterTarget struct {
	Host  string
	Port  int
	Queue string // IPP path on the printer (e.g. "ipp/print" for Brother, "printers/<name>" for CUPS).
}

// NewIPPPrinter returns a Printer that delivers jobs to a network printer over
// IPP (port 631 by default).
//
// Security note: this client speaks plain (unauthenticated) IPP and is only
// suitable for printers reachable on the lab's trusted LAN. The print queue
// host/port are configured by leadership through the admin UI; do not point
// it at anything routable from the public internet.
func NewIPPPrinter(target PrinterTarget) Printer {
	return &ippPrinter{target: target}
}

type ippPrinter struct {
	target PrinterTarget
}

// brotherAdapter wraps the stock HttpAdapter so the HTTP request URL uses the
// configured printer path verbatim instead of the library's hardcoded
// "/printers/<queue>" namespace. Brother printers (and many other embedded
// IPP servers) expose only a single endpoint such as "/ipp/print" and return
// 404 for the CUPS-style "/printers/..." path.
//
// The IPP-level "printer-uri" attribute inside the request body is built
// separately by the client (`ipp://localhost/printers/<queue>`) and is
// independent of the HTTP URL the request is POSTed to, so overriding only
// GetHttpUri is sufficient.
type brotherAdapter struct {
	*ipp.HttpAdapter
	scheme string
	host   string
	port   int
	path   string // already URL-quoted-safe; no leading slash
}

func (a *brotherAdapter) GetHttpUri(_ string, _ interface{}) string {
	return fmt.Sprintf("%s://%s:%d/%s", a.scheme, a.host, a.port, a.path)
}

func (p *ippPrinter) Print(ctx context.Context, job PrintJob) error {
	if p.target.Host == "" || p.target.Queue == "" {
		return fmt.Errorf("printer not configured (missing host or queue)")
	}
	port := p.target.Port
	if port == 0 {
		port = 631
	}

	// Strip any leading slash so a user pasting "/ipp/print" still works.
	queuePath := strings.TrimLeft(p.target.Queue, "/")

	adapter := &brotherAdapter{
		HttpAdapter: ipp.NewHttpAdapter(p.target.Host, port, "conway", "", false),
		scheme:      "http",
		host:        p.target.Host,
		port:        port,
		path:        queuePath,
	}
	client := ipp.NewIPPClientWithAdapter("conway", adapter)

	doc := ipp.Document{
		Document: bytes.NewReader(job.PDF),
		Size:     len(job.PDF),
		Name:     job.JobName,
		MimeType: "application/pdf",
	}

	// PrintJobContext uses the queue name only to populate the IPP
	// "printer-uri" attribute; the HTTP URL comes from our adapter above.
	if _, err := client.PrintJobContext(ctx, doc, queuePath, nil); err != nil {
		return fmt.Errorf("ipp print: %w", err)
	}
	return nil
}

// noopPrinter is used when the module has no real printer configured.
// It errors on Print so jobs back off in the queue rather than being
// silently dropped. The error is intentionally short — the workqueue
// already logs every failure with context.
type noopPrinter struct{}

func (noopPrinter) Print(ctx context.Context, job PrintJob) error {
	return fmt.Errorf("printer not configured")
}
