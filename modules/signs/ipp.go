package signs

import (
	"bytes"
	"context"
	"fmt"

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
	Queue string // Printer/queue name (the part after /printers/ in the IPP URI).
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

func (p *ippPrinter) Print(ctx context.Context, job PrintJob) error {
	if p.target.Host == "" || p.target.Queue == "" {
		return fmt.Errorf("printer not configured (missing host or queue)")
	}
	port := p.target.Port
	if port == 0 {
		port = 631
	}

	client := ipp.NewIPPClient(p.target.Host, port, "conway", "", false)
	doc := ipp.Document{
		Document: bytes.NewReader(job.PDF),
		Size:     len(job.PDF),
		Name:     job.JobName,
		MimeType: "application/pdf",
	}

	if _, err := client.PrintJobContext(ctx, doc, p.target.Queue, nil); err != nil {
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
