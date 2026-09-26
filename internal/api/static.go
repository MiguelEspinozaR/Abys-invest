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
	// M5: /watchlist es espacio de nombres de la API (GET lista, PUT/DELETE
	// por ticker) y, a la vez, la ruta de la página dedicada del SPA. El
	// prefijo solo protege los paths más profundos (/watchlist/extra → 404
	// JSON, nunca index.html), igual que /alerts. La colisión de la ruta
	// EXACTA se resuelve por negociación de contenido en el handler del
	// endpoint (plan M5 §B4, decisión del orquestador 2026-09-25): ver
	// serveSPAIndexNegotiated.
	"watchlist":     {},
	"refresh":       {},
	"force-refresh": {},
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
		serveIndexFile(w, r, staticDir)
	}
}

// serveIndexFile sirve el index.html del build con el mismo mecanismo que el
// fallback SPA (http.ServeFile sobre el archivo real de staticDir).
func serveIndexFile(w http.ResponseWriter, r *http.Request, staticDir string) {
	http.ServeFile(w, r, indexPath(staticDir))
}

// indexPath es la ruta del shell del SPA dentro de staticDir (única fuente de
// verdad compartida por la raíz, el fallback y la negociación de contenido).
func indexPath(staticDir string) string { return filepath.Join(staticDir, "index.html") }

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
		serveIndexFile(w, r, staticDir)
	}
}

// serveSPAIndexNegotiated sirve index.html cuando el cliente declara aceptar
// HTML y hay un build del frontend disponible; devuelve true si ya respondió
// (el llamante no debe escribir nada más).
//
// Por qué existe (plan M5 §B4, decisión del orquestador 2026-09-25): el
// despliegue es same-origin —el propio API Go sirve web/dist (plan M4 D2)— y
// /watchlist es a la vez endpoint JSON (CA-M5-2) y página del SPA (CA-M5-3).
// El patrón exacto "GET /watchlist" es más específico que el fallback
// "GET /{path...}", así que ganaba siempre y un F5 o una carga directa de esa
// URL mostraban el JSON de la lista en crudo en el navegador. Ahora se negocia
// por Accept: el navegador (document navigation → "text/html,...") recibe el
// shell del SPA y el cliente de la API (getJSON → "application/json") sigue
// recibiendo el array, de modo que el contrato REST no cambia. Es el mismo
// mecanismo (serveIndexFile) que usan la raíz y el fallback del SPA; no se
// duplica lógica de serving.
//
// Si no hay staticDir configurado (API sin frontend) o el index.html no existe,
// devuelve false y el llamante continúa con su respuesta habitual (JSON): en
// despliegues sin build del frontend el comportamiento es el previo.
//
// La respuesta declara Vary: Accept porque GET /watchlist es dual (navegador →
// shell del SPA, cliente de la API → array JSON) y las cachés deben guardar
// ambas variantes por separado. La otra mitad de la negociación (la respuesta
// JSON de handleListWatchlist) también lo declara.
func serveSPAIndexNegotiated(w http.ResponseWriter, r *http.Request, staticDir string) bool {
	if staticDir == "" || !wantsHTML(r) {
		return false
	}
	fi, err := os.Stat(indexPath(staticDir))
	if err != nil || fi.IsDir() {
		return false
	}
	w.Header().Set("Vary", "Accept")
	serveIndexFile(w, r, staticDir)
	return true
}

// wantsHTML indica que el cliente acepta HTML. Los navegadores mandan
// "text/html,..." en navegación directa/F5; los clientes JSON de la SPA mandan
// "application/json" y curl sin header manda "*/*": ambos siguen recibiendo la
// respuesta JSON de la API.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
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
