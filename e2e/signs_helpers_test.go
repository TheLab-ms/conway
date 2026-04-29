package e2e

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/modules/signs"
	"github.com/stretchr/testify/require"
)

// fakePrinter captures Print calls for assertion in tests. It mirrors the
// fakePrinter used by modules/signs/module_test.go.
type fakePrinter struct {
	mu   sync.Mutex
	jobs []signs.PrintJob
	err  error
}

func (f *fakePrinter) Print(ctx context.Context, job signs.PrintJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := signs.PrintJob{JobName: job.JobName, PDF: append([]byte(nil), job.PDF...)}
	f.jobs = append(f.jobs, cp)
	return f.err
}

func (f *fakePrinter) Jobs() []signs.PrintJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]signs.PrintJob, len(f.jobs))
	copy(out, f.jobs)
	return out
}

func (f *fakePrinter) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = nil
}

// seedSignsConfig inserts (or updates) a row into signs_config with the
// default maintenance template and the supplied IPP target. Idempotent —
// if the module already inserted a default row on first load we update it
// rather than stacking another version that would shadow the first.
func seedSignsConfig(t *testing.T, env *TestEnv, host string, port int, queue string) {
	t.Helper()
	tmplBytes, err := json.Marshal([]signs.Template{signs.DefaultMaintenanceTemplate})
	require.NoError(t, err, "could not marshal templates_json")

	var existingVersion int
	err = env.db.QueryRow(`SELECT version FROM signs_config ORDER BY version DESC LIMIT 1`).Scan(&existingVersion)
	if err == nil {
		_, err = env.db.Exec(
			`UPDATE signs_config
			   SET printer_host = ?, printer_port = ?, printer_queue = ?, templates_json = ?
			 WHERE version = ?`,
			host, port, queue, string(tmplBytes), existingVersion)
		require.NoError(t, err, "could not update signs config")
		return
	}
	_, err = env.db.Exec(
		`INSERT INTO signs_config (printer_host, printer_port, printer_queue, templates_json)
		 VALUES (?, ?, ?, ?)`,
		host, port, queue, string(tmplBytes))
	require.NoError(t, err, "could not insert signs config")
}
