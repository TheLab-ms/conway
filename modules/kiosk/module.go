package kiosk

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
)

// TODO: Replace?

//go:generate go run github.com/a-h/templ/cmd/templ generate

const qrTTL = time.Minute * 5 // length of time a signed QR code is valid

type Module struct {
	db     *sql.DB
	self   *url.URL
	signer *engine.TokenIssuer

	trustedHostname string
	trustedIP       atomic.Pointer[net.IP]
}

func New(db *sql.DB, self *url.URL, iss *engine.TokenIssuer, trustedHostname string) *Module {
	return &Module{db: db, self: self, signer: iss, trustedHostname: trustedHostname}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.findTrustedIP))
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /kiosk", m.atPhysicalSpace(m.renderKiosk))
	router.HandleFunc("GET /keyfob/bind", router.WithAuthn(m.handleBindKeyfob))
	router.HandleFunc("GET /keyfob/status/{id}", m.atPhysicalSpace(m.handleGetKeyFobInUse))
}

// findTrustedIP sets trustedIP by resolving trustedHostname.
// This is used to make sure that only
func (m *Module) findTrustedIP(ctx context.Context) bool {
	conn, err := net.Dial("udp4", m.trustedHostname+":80") // any port will do
	if err != nil {
		slog.Error("unable to dial trusted hostname", "error", err)
		return false
	}
	conn.Close()

	ip := conn.RemoteAddr().(*net.UDPAddr).IP
	old := m.trustedIP.Swap(&ip)

	if old == nil || !old.Equal(ip) {
		slog.Info("updated trusted IP", "new", ip)
	}

	return false
}

func (m *Module) atPhysicalSpace(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only allow fobs to be assigned at the makerspace
		addr := r.Header.Get("CF-Connecting-IP")
		if addr == "" {
			addr = r.RemoteAddr
		}
		ip := net.ParseIP(strings.Split(addr, ":")[0])
		if trusted := m.trustedIP.Load(); trusted == nil || !ip.Equal(*trusted) {
			slog.Info("not allowing member to bind keyfob from this IP", "addr", addr, "ip", ip, "trusted", trusted)
			w.Header().Set("Content-Type", "text/html")
			renderOffsiteError().Render(r.Context(), w)
			return
		}
		next(w, r)
	}
}

func (m *Module) renderKiosk(w http.ResponseWriter, r *http.Request) {
	idStr := r.FormValue("fobid")
	var png []byte
	if idStr != "" {
		tok, err := m.signer.Sign(&jwt.RegisteredClaims{
			Subject:   idStr,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(qrTTL)),
		})
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}

		url := fmt.Sprintf("%s/keyfob/bind?val=%s", m.self, url.QueryEscape(tok))
		p, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		png = make([]byte, base64.StdEncoding.EncodedLen(len(p)))
		base64.StdEncoding.Encode(png, p)
	}
	w.Header().Set("Content-Type", "text/html")
	renderKiosk(png).Render(r.Context(), w)
}

func (m *Module) handleBindKeyfob(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserMeta(r.Context())

	claims, err := m.signer.Verify(r.FormValue("val"))
	if err != nil {
		http.Error(w, "Invalid QR code", 400)
		return
	}
	fobID, err := strconv.ParseInt(claims.Subject, 10, 0)
	if err != nil {
		http.Error(w, "QR code references a non-integer fob ID", 400)
		return
	}

	_, err = m.db.ExecContext(r.Context(), `UPDATE members SET fob_id = $1 WHERE id = $2 AND (fob_id IS NULL OR fob_id != $1)`, fobID, user.ID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	slog.Info("bound keyfob to member", "fobid", fobID, "memberID", user.ID)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (m *Module) handleGetKeyFobInUse(w http.ResponseWriter, r *http.Request) {
	fobID, err := strconv.ParseInt(r.PathValue("id"), 10, 0)
	if err != nil {
		http.Error(w, "Invalid fob ID", 400)
		return
	}

	var count int
	err = m.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM members WHERE fob_id = $1", fobID).Scan(&count)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(count > 0)
}
