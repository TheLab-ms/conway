package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/julienschmidt/httprouter"
)

//go:generate templ generate

type Handler func(r *http.Request, ps httprouter.Params) Response

type Response interface {
	write(http.ResponseWriter, *http.Request) error
}

type Authenticator interface {
	WithAuth(Handler) Handler
}

type noopAuthenticator struct{}

func (noopAuthenticator) WithAuth(fn Handler) Handler { return fn }

type Router struct {
	router *httprouter.Router

	// Authenticator can be used to pass an authenticator implementation to other handlers.
	Authenticator
}

func NewRouter(notFoundHandler http.Handler) *Router {
	r := &Router{router: httprouter.New()}
	r.router.NotFound = notFoundHandler
	r.Authenticator = noopAuthenticator{}
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, rr *http.Request) { r.router.ServeHTTP(w, rr) }

func (r *Router) Handle(method, path string, fn Handler) {
	r.router.Handle(method, path, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		invokeHandler(w, r, p, fn)
	})
}

func invokeHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params, fn Handler) {
	start := time.Now()
	resp := fn(r, p)
	logger := slog.Default().With("url", r.URL.Path, "method", r.Method, "userAgent", r.UserAgent(), "latencyMS", time.Since(start).Milliseconds())

	e, _ := resp.(*httpError)
	if e == nil {
		if !strings.HasPrefix(r.URL.Path, "/api/peering") { // suppress noisy Glider logs
			logger.Info("handled http request")
		}
	} else {
		logger.Error("handled http request", "error", e.Message, "details", e.DetailedMessage, "status", e.StatusCode)
	}

	if resp == nil {
		return
	}
	if err := resp.write(w, r); err != nil {
		slog.Warn("error while writing http response", "error", err)
		return
	}
}

type httpError struct {
	DetailedMessage string // logged, not returned
	Message         string // shared with the client
	StatusCode      int    // http status code i.e. 200
}

func (e *httpError) write(w http.ResponseWriter, r *http.Request) error {
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.StatusCode)
		return json.NewEncoder(w).Encode(map[string]string{"error": e.Message})
	}

	w.Header().Set("Content-Type", "text/html")
	return renderError(e).Render(r.Context(), w)
}

func Errorf(templ string, args ...any) Response {
	return &httpError{
		DetailedMessage: fmt.Sprintf(templ, args...),
		Message:         "Internal error",
		StatusCode:      500,
	}
}

func ClientErrorf(status int, templ string, args ...any) Response {
	msg := fmt.Sprintf(templ, args...)
	return &httpError{
		DetailedMessage: msg,
		Message:         msg,
		StatusCode:      status,
	}
}

func Error(err error) Response {
	if err == nil {
		return nil
	}
	return Errorf("%s", err)
}

type httpRedirect struct {
	URL  string
	Code int
}

func Redirect(url string, code int) *httpRedirect { return &httpRedirect{URL: url, Code: code} }

func (rr *httpRedirect) write(w http.ResponseWriter, r *http.Request) error {
	http.Redirect(w, r, rr.URL, rr.Code)
	return nil
}

type jsonResponse struct {
	Value any
}

func JSON(val any) Response { return &jsonResponse{Value: val} }

func (j *jsonResponse) write(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(j.Value)
}

type componentResponse struct {
	Component templ.Component
}

func Component(comp templ.Component) Response {
	return &componentResponse{Component: comp}
}

func (c *componentResponse) write(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/html")
	return c.Component.Render(r.Context(), w)
}

type cookieResponse struct {
	Cookie *http.Cookie
	Next   Response
}

func WithCookie(cook *http.Cookie, next Response) Response {
	return &cookieResponse{Cookie: cook, Next: next}
}

func (c *cookieResponse) write(w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, c.Cookie)
	return c.Next.write(w, r)
}

type emptyResponse struct {
}

func Empty() Response { return &emptyResponse{} }

func (*emptyResponse) write(w http.ResponseWriter, r *http.Request) error {
	w.WriteHeader(204)
	return nil
}

type pngResponse struct {
	buf []byte
}

func PNG(buf []byte) Response { return &pngResponse{buf: buf} }

func (p *pngResponse) write(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "image/png")
	_, err := w.Write(p.buf)
	return err
}
