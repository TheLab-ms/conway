package engine

import (
	"database/sql"
	"fmt"
	"net/http"
)

// ServeHealthProbe returns an HTTP handler that verifies db is reachable by
// opening and rolling back an empty transaction. It responds 200 on success
// and 500 on any error.
func ServeHealthProbe(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		txn, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		if err := txn.Rollback(); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}
}

// CheckHealthProbe is the client counterpart to ServeHealthProbe: it issues an
// HTTP GET against url and returns nil iff the response status is 200.
func CheckHealthProbe(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}
