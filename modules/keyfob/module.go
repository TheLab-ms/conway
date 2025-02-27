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
	"crypto/rand"
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
	"github.com/julienschmidt/httprouter"
	"github.com/skip2/go-qrcode"
)

//go:generate templ generate

const sigTTL = time.Minute * 5 // length of time a signed QR code is valid

type Module struct {
	db         *sql.DB
	self       *url.URL
	signingKey []byte

	trustedHostname string
	trustedIP       atomic.Pointer[net.IP]
}

func New(db *sql.DB, self *url.URL, trustedHostname string) *Module {
	m := &Module{db: db, self: self, trustedHostname: trustedHostname}
	m.initSigningKey()
	return m
}

func (m *Module) initSigningKey() {
	m.signingKey = make([]byte, 32)
	if _, err := rand.Read(m.signingKey); err != nil {
		panic(err)
	}
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
		id, err := strconv.ParseInt(idStr, 10, 0)
		if err != nil {
			return engine.ClientErrorf(400, "Invalid fob ID")
		}

		url := fmt.Sprintf("%s/keyfob/bind?val=%s", m.self, url.QueryEscape(engine.IntSigner.Sign(id, m.signingKey, sigTTL)))
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

	fobID, ok := engine.IntSigner.Verify(r.FormValue("val"), m.signingKey)
	if !ok {
		return engine.ClientErrorf(400, "Invalid QR code")
	}

	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET fob_id = $1 WHERE id = $2", fobID, user.ID)
	if err != nil {
		return engine.ClientErrorf(500, "inserting fob id into db: %s", err)
	}
	slog.Info("bound keyfob to member", "fobid", fobID, "memberID", user.ID)

	return engine.Redirect("/", http.StatusSeeOther)
}
