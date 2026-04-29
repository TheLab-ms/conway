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
	port := target.Port
	if port == 0 {
		port = 631
	}
	// Trim slashes so a user pasting "/ipp/print" or "ipp/print/" still works.
	queuePath := strings.Trim(target.Queue, "/")

	adapter := &brotherAdapter{
		HttpAdapter: ipp.NewHttpAdapter(target.Host, port, ippUsername, "", false),
		scheme:      "http",
		host:        target.Host,
		port:        port,
		path:        queuePath,
	}
	// Built once and reused so the underlying *http.Transport pools
	// connections across jobs (it would otherwise leak idle conns under load).
	url := adapter.GetHttpUri("", nil)
	printerURI := fmt.Sprintf("ipp://%s:%d/%s", target.Host, port, queuePath)

	return &ippPrinter{
		target:     target,
		adapter:    adapter,
		url:        url,
		printerURI: printerURI,
	}
}

// ippUsername is sent as the IPP "requesting-user-name" attribute. The printer
// shows it in the queue UI; nothing on the lab LAN authenticates against it.
const ippUsername = "conway"

type ippPrinter struct {
	target     PrinterTarget
	adapter    *brotherAdapter
	url        string // HTTP URL we POST IPP requests to
	printerURI string // value of the IPP "printer-uri" operation attribute
}

// brotherAdapter wraps the stock HttpAdapter so the HTTP request URL uses the
// configured printer path verbatim instead of the library's hardcoded
// "/printers/<queue>" namespace. Brother printers (and many other embedded
// IPP servers) expose only a single endpoint such as "/ipp/print" and return
// 404 for the CUPS-style "/printers/..." path.
type brotherAdapter struct {
	*ipp.HttpAdapter
	scheme string
	host   string
	port   int
	path   string
}

func (a *brotherAdapter) GetHttpUri(_ string, _ interface{}) string {
	return fmt.Sprintf("%s://%s:%d/%s", a.scheme, a.host, a.port, a.path)
}

func (p *ippPrinter) Print(ctx context.Context, job PrintJob) error {
	if p.target.Host == "" || p.target.Queue == "" {
		return fmt.Errorf("printer not configured (missing host or queue)")
	}
	if len(job.PDF) == 0 {
		return fmt.Errorf("print job has empty PDF")
	}

	// Build the Print-Job request directly so we can set "printer-uri" to
	// the printer's actual IPP URI. The library's PrintJobContext hardcodes
	// it to "ipp://localhost/printers/<queue>", which Brother rejects with
	// IPP status 0x0406 (client-error-not-found).
	req := ipp.NewRequest(ipp.OperationPrintJob, 1)
	req.OperationAttributes[ipp.AttributePrinterURI] = p.printerURI
	req.OperationAttributes[ipp.AttributeRequestingUserName] = ippUsername
	req.OperationAttributes[ipp.AttributeJobName] = job.JobName
	req.OperationAttributes[ipp.AttributeDocumentFormat] = "application/pdf"
	req.OperationAttributes[ipp.AttributeCopies] = 1
	req.OperationAttributes[ipp.AttributeJobPriority] = ipp.DefaultJobPriority
	req.File = bytes.NewReader(job.PDF)
	req.FileSize = len(job.PDF)

	// We discard the response (which carries the printer-assigned job ID);
	// the workqueue tracks our own job identity and we do not poll status.
	if _, err := p.adapter.SendRequestContext(ctx, p.url, req, nil); err != nil {
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
