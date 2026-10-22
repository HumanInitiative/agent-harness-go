package httpapi

import (
	"embed"
	"net/http"
)

//go:embed reportpage
var reportPageFiles embed.FS

// reportPageAssets maps a URL path to an embedded file and its media type.
var reportPageAssets = map[string]struct{ file, contentType string }{
	"/reports":         {"reportpage/index.html", "text/html; charset=utf-8"},
	"/reports/app.js":  {"reportpage/app.js", "text/javascript; charset=utf-8"},
	"/reports/app.css": {"reportpage/app.css", "text/css; charset=utf-8"},
}

// reportPageCSP lets the page load only its own script and style and talk
// only to this server; it cannot be framed.
const reportPageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// ReportPage serves the internal page for drafting a donation report: a
// form that calls the API with a key the person types. The page is static
// and holds no secret, so it needs no authentication itself; every call it
// makes does.
func ReportPage() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset, ok := reportPageAssets[r.URL.Path]
		if !ok {
			writeError(w, r, http.StatusNotFound, codeNotFound, "no such endpoint")
			return
		}
		body, err := reportPageFiles.ReadFile(asset.file)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
			return
		}
		h := w.Header()
		h.Set("Content-Type", asset.contentType)
		h.Set("Content-Security-Policy", reportPageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}
