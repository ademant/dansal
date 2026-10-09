package main

import (
	_ "embed"
	"net/http"
)

// qrcodeJS is the same vendored library cmd/dansal_web already self-hosts
// (#1448, compliance G11) -- copied rather than fetched again, since it's
// already the exact bytes previously loaded from unpkg.com in users.html.
// No minify/gzip/brotli precompute here unlike dansal_web's base.js/Leaflet
// treatment: this loads once per admin doing TOTP setup, not on every public
// page hit, so the extra machinery isn't worth it.
//
//go:embed static/qrcode.min.js
var qrcodeJS []byte

func qrcodeJSHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(qrcodeJS)
}
