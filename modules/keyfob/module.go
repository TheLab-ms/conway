// Keyfob is responsible for scanning and binding physical RFID fobs to member accounts.
//
// It does this securely by only accepting fob IDs sent from the makerspace's public IP
// and signing a string containing the trusted keyfob id, which can then be transferred
// over a less trusted channel (the internet) to the member's device.
//
// This obviously isn't perfect, but it's plenty good considering the tensile strength of drywall.
package keyfob

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	"github.com/julienschmidt/httprouter"
	"github.com/skip2/go-qrcode"
)

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
	router.Handle("GET", "/kiosk", m.atPhysicalSpace(m.renderKiosk))
	router.Handle("GET", "/keyfob/bind", router.WithAuth(m.handleBindKeyfob))
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

func (m *Module) atPhysicalSpace(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		// Only allow fobs to be assigned at the makerspace
		addr := r.Header.Get("CF-Connecting-IP")
		if addr == "" {
			addr = r.RemoteAddr
		}
		ip := net.ParseIP(strings.Split(addr, ":")[0])
		if trusted := m.trustedIP.Load(); trusted == nil || !ip.Equal(*trusted) {
			slog.Info("not allowing member to bind keyfob from this IP", "addr", addr, "ip", ip, "trusted", trusted)
			return engine.Component(renderOffsiteError())
		}
		return next(r, ps)
	}
}

func (m *Module) renderKiosk(r *http.Request, ps httprouter.Params) engine.Response {
	idStr := r.FormValue("fobid")
	var png []byte
	if idStr != "" {
		tok, err := m.signer.Sign(&jwt.RegisteredClaims{
			Subject:   idStr,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(qrTTL)),
		})

		url := fmt.Sprintf("%s/keyfob/bind?val=%s", m.self, url.QueryEscape(tok))
		p, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			return engine.Error(err)
		}
		png = make([]byte, base64.StdEncoding.EncodedLen(len(p)))
		base64.StdEncoding.Encode(png, p)
	}
	return engine.Component(renderKiosk(png))
}

func (m *Module) handleBindKeyfob(r *http.Request, ps httprouter.Params) engine.Response {
	user := auth.GetUserMeta(r.Context())

	claims, err := m.signer.Verify(r.FormValue("val"))
	if err != nil {
		return engine.ClientErrorf(400, "Invalid QR code")
	}
	fobID, err := strconv.ParseInt(claims.Subject, 10, 0)
	if err != nil {
		return engine.ClientErrorf(400, "QR code references a non-integer fob ID")
	}

	_, err = m.db.ExecContext(r.Context(), "UPDATE members SET fob_id = $1 WHERE id = $2", fobID, user.ID)
	if err != nil {
		return engine.ClientErrorf(500, "inserting fob id into db: %s", err)
	}
	slog.Info("bound keyfob to member", "fobid", fobID, "memberID", user.ID)

	return engine.Redirect("/", http.StatusSeeOther)
}
