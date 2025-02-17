package keyfob

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

//go:generate templ generate

type Module struct {
	db *sql.DB

	trustedHostname string
	trustedIP       atomic.Pointer[net.IP]
}

func New(db *sql.DB, trustedHostname string) *Module {
	return &Module{db: db, trustedHostname: trustedHostname}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.findTrustedIP))
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/keyfob/bind", router.WithAuth(m.atPhysicalSpace(m.renderBindKeyfob)))
	router.Handle("POST", "/keyfob/bind", router.WithAuth(m.atPhysicalSpace(m.handleBindKeyfob)))
}

// findTrustedIP sets trustedIP by resolving trustedHostname.
// This is used to make sure that only
func (m *Module) findTrustedIP(ctx context.Context) bool {
	conn, err := net.Dial("udp", m.trustedHostname+":80") // any port will do
	if err != nil {
		slog.Error("unable to dial trusted hostname", "error", err)
		return false
	}
	conn.Close()

	ip := conn.RemoteAddr().(*net.UDPAddr).IP
	m.trustedIP.Store(&ip)
	return false
}

func (m *Module) atPhysicalSpace(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		// Only allow fobs to be assigned at the makerspace
		addr := r.Header.Get("CF-Connecting-IP")
		if addr == "" {
			addr = r.RemoteAddr
		}
		ipStr, _, _ := net.SplitHostPort(addr)
		ip := net.ParseIP(ipStr)
		if trusted := m.trustedIP.Load(); trusted == nil || !ip.Equal(*trusted) {
			return engine.Component(renderOffsiteError())
		}
		return next(r, ps)
	}
}

func (m *Module) renderBindKeyfob(r *http.Request, ps httprouter.Params) engine.Response {
	failed := r.URL.Query().Get("e") == "rror" // lol "e"+"rror"
	return engine.Component(renderKeyfob(failed))
}

func (m *Module) handleBindKeyfob(r *http.Request, ps httprouter.Params) engine.Response {
	user := auth.GetUserMeta(r.Context())

	id := r.PostFormValue("fobid")
	slog.Info("attempting to bind keyfob", "fobid", id, "memberID", user.ID)

	fobID, err := strconv.Atoi(id)
	if err != nil {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}

	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return engine.Errorf("starting db txn: %s", err)
	}
	defer tx.Rollback()

	var exists bool
	err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM members WHERE fob_id = ? AND id != ?)", fobID, user.ID).Scan(&exists)
	if err != nil {
		return engine.Errorf("checking for existing fob: %s", err)
	}
	if exists {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}

	_, err = tx.ExecContext(r.Context(), "UPDATE members SET fob_id = ? WHERE id = ?", fobID, user.ID)
	if err != nil {
		return engine.Errorf("binding fob to member: %s", err)
	}

	err = tx.Commit()
	if err != nil {
		return engine.Errorf("committing db txn: %s", err)
	}

	return engine.Redirect("/", http.StatusSeeOther)
}
