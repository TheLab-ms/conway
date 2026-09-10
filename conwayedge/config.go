package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
)

func parsePrinters(data []byte) ([]printerConfig, error) {
	var printers []printerConfig
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&printers); err != nil {
		return nil, fmt.Errorf("invalid printer JSON: %w", err)
	}
	if printers == nil || d.Decode(new(any)) != io.EOF || len(printers) > 32 {
		return nil, fmt.Errorf("expected one array of at most 32 printers")
	}
	seen := make(map[string]bool)
	for _, p := range printers {
		// Literal addresses keep local configuration unambiguous; ports are fixed.
		if p.Name == "" || net.ParseIP(p.Host) == nil || p.AccessCode == "" || p.SerialNumber == "" ||
			strings.ContainsAny(p.SerialNumber, "/+# \t\r\n") || seen[p.SerialNumber] {
			return nil, fmt.Errorf("each printer needs a name, IP address, access_code, and unique MQTT-safe serial_number")
		}
		seen[p.SerialNumber] = true
	}
	return printers, nil
}

var configPage = template.Must(template.New("config").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>conwayedge configuration</title>
<style>body{font:16px system-ui;margin:2rem auto;padding:0 1rem;max-width:55rem}textarea{box-sizing:border-box;width:100%;font:14px monospace}button{padding:.6rem 1.2rem}</style>
<h1>conwayedge</h1><h2>Printers</h2>
<p>Each printer needs <code>name</code>, <code>host</code> (IP address), <code>access_code</code>, and <code>serial_number</code>. Use <code>[]</code> to remove all printers. Status is polled every five seconds.</p>
<form method="post" action="/config"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label for="printers">Printer JSON (includes passwords)</label><p><textarea id="printers" name="printers" rows="18" spellcheck="false" required>{{.JSON}}</textarea></p>
<button type="submit">Save</button></form></html>`))

func (e *edge) configure(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	switch r.Method {
	case http.MethodGet:
		e.mu.Lock()
		data, _ := json.MarshalIndent(e.config, "", "  ")
		e.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = configPage.Execute(w, struct{ CSRF, JSON string }{e.csrf, string(data)})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form (limit 64 KiB)", http.StatusBadRequest)
			return
		}
		if !secretEqual(r.PostForm.Get("csrf"), e.csrf) {
			http.Error(w, "invalid form token", http.StatusForbidden)
			return
		}
		printers, err := parsePrinters([]byte(r.PostForm.Get("printers")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := json.MarshalIndent(printers, "", "  ")
		e.mu.Lock()
		defer e.mu.Unlock()
		if err := atomicWrite(filepath.Join(e.dir, "printers.json"), append(data, '\n')); err != nil {
			log.Printf("persist printers: %v", err)
			http.Error(w, "cannot persist printers", http.StatusInternalServerError)
			return
		}
		e.config = printers
		e.printers.replace(printers)
		http.Redirect(w, r, "/config", http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
