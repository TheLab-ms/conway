package keyfob

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

type Module struct {
	db         *sql.DB
	self       *url.URL
	signingKey []byte

	trustedHostname string
	trustedIP       atomic.Pointer[net.IP]
}

func New(db *sql.DB, self *url.URL, trustedHostname string) *Module {
	key := make([]byte, 32)

	if _, err := rand.Read(key); err != nil {
		panic(err)
	}

	return &Module{db: db, self: self, signingKey: key, trustedHostname: trustedHostname}
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
	id := r.FormValue("fobid")
	var png []byte
	if id != "" {
		h := hmac.New(sha256.New, m.signingKey)
		h.Write([]byte(id))

		val := fmt.Sprintf("%s.%s", id, base64.StdEncoding.EncodeToString(h.Sum(nil)))
		url := fmt.Sprintf("%s/keyfob/bind?val=%s", m.self, val)
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

	// Parse the value+signature string
	val := r.FormValue("val")
	parts := strings.Split(val, ".")
	if len(parts) != 2 {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}

	// Verify the signature
	id := parts[0]
	sig, _ := base64.StdEncoding.DecodeString(parts[1])
	h := hmac.New(sha256.New, m.signingKey)
	h.Write([]byte(id))

	if !hmac.Equal(sig, h.Sum(nil)) {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}

	fobID, err := strconv.Atoi(id)
	if err != nil {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}

	// Assign it!
	_, err = m.db.ExecContext(r.Context(), "UPDATE members SET fob_id = ? WHERE id = ?", fobID, user.ID)
	if err != nil {
		return engine.Redirect("/keyfob/bind?e=rror", http.StatusSeeOther)
	}
	slog.Info("bound keyfob to member", "fobid", fobID, "memberID", user.ID)

	return engine.Redirect("/", http.StatusSeeOther)
}
