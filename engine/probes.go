package engine

import (
	"database/sql"
	"fmt"
	"net/http"
)

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
