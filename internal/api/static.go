package api

import (
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// apiRouteSegments son los prefijos (primer segmento) reservados por la API
// (ver router.go y M5 /alerts). El fallback SPA nunca debe responder
// index.html para un path con uno de estos prefijos: una ruta API desconocida
// o malformada (p. ej. /score/UNKNOWN/extra o /securities/) mantiene el
// envelope JSON 404 en vez de ser interceptada por el fallback (riesgo M4-R2).
var apiRouteSegments = map[string]struct{}{
	"health":     {},
	"securities": {},
	"prices":     {},
	"metrics":    {},
	"valuation":  {},
	"score":      {},
	"scores":     {},
	"compare":    {},
	"backtest":   {},
	"alerts":     {},
}

// RegisterStatic monta el serving de estáticos del frontend (plan M4, D2)
// sobre el MISMO ServeMux que las rutas API: GET /static/* se sirve con
// http.FileServer, GET / con serveIndex y el resto cae en el SPA fallback
// GET /{path...}. staticDir (env STATIC_DIR, default ./web/dist en cmd/api)
// puede no existir todavía (build del frontend pendiente): en ese caso no se
// registra nada y las rutas API conservan su comportamiento exacto.
func RegisterStatic(mux *http.ServeMux, staticDir string) {
	info, err := os.Stat(staticDir)
	if err != nil || !info.IsDir() {
		slog.Warn("estáticos no disponibles (STATIC_DIR no existe)", "dir", staticDir)
		return
	}
	slog.Info("estáticos habilitados", "dir", staticDir)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("GET /{$}", serveIndex(staticDir))
	mux.HandleFunc("GET /{path...}", spaHandler(staticDir))
}

// serveIndex sirve index.html del build para la raíz del SPA (GET /).
func serveIndex(staticDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	}
}

// spaHandler es el fallback del SPA (plan M4 D2): responde un archivo real de
// staticDir si existe y, si no, index.html (routing del react-router, p. ej.
// /ticker/AAPL). Los paths bajo un prefijo reservado de la API devuelven el
// envelope JSON 404 (nunca index.html). Como defensa extra, el path se
// normaliza y se verifica que el archivo quede dentro de staticDir (ServeMux
// ya redirige los ".." 307 antes de llegar aquí, pero no se confía en ello).
func spaHandler(staticDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isAPIRoute(r.URL.Path) {
			writeError(w, http.StatusNotFound, CodeNotFound, "ruta no encontrada: %s", r.URL.Path)
			return
		}
		if full := resolveWithin(staticDir, r.URL.Path); full != "" {
			if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
				http.ServeFile(w, r, full)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	}
}

// resolveWithin devuelve el path absoluto de reqPath dentro de root, o "" si
// el resultado escaparía de root. reqPath se normaliza como ruta absoluta
// (un /../x se convierte en /x) y la contención se comprueba con filepath.Rel.
func resolveWithin(root, reqPath string) string {
	clean := path.Clean("/" + strings.TrimPrefix(reqPath, "/"))
	full := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return full
}

// isAPIRoute determina si el path cae en un espacio de nombres reservado de la
// API (primer segmento conocido); esos paths nunca llegan al fallback del SPA.
func isAPIRoute(reqPath string) bool {
	seg, _, _ := strings.Cut(strings.TrimPrefix(reqPath, "/"), "/")
	_, ok := apiRouteSegments[seg]
	return ok
}
