// Runner de jobs de pipeline en segundo plano (plan M5.1, SPEC §6 M5.1 y §11ter).
//
// Por qué existe: antes, POST /force-refresh ejecutaba el pipeline con
// r.Context(). Si el navegador navegaba o recargaba durante el trabajo (~1 min),
// Go cancelaba el contexto, Yahoo devolvía "context canceled" y el usuario veía
// un 500/"failed to fetch" con la ingesta a medias. Aquí el job corre en una
// goroutine con contexto PROPIO (context.Background + timeout de 30 min), así
// que sobrevive al request y su progreso es consultable en GET
// /pipeline/status (jobs.status()).
//
// Semántica (todo bajo r.mu):
//   - ocupación = r.sync (un POST /refresh síncrono) o un job no terminal;
//   - POST /force-refresh → start (409 si ocupada); PUT /watchlist/{ticker} →
//     requestIngest, que NUNCA falla: si está ocupada encola el ticker (FIFO),
//     y el runner drena la cola encadenando UN job "watchlist" con todos los
//     pendientes (D1: merge mutaría el universo a mitad de ejecución);
//   - el progreso se publica por etapas vía stepFunc, con guard de generación
//     para que un job viejo no pise el estado del job actual.
//
// El estado vive EN MEMORIA del proceso (app local single-user, SPEC §2.3): un
// reinicio del API pierde el job en curso y /pipeline/status vuelve a "idle"
// (riesgo R1 del plan, aceptado). El paquete api no mantiene otro estado
// global: los tests pueden sustituir `jobs` por un runner con exec fake.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/pipeline"
)

const (
	// JobKindWatchlist es la ingesta parcial de un ticker (o de la cola) de la
	// watchlist: prices → sector → metrics → scores, sin EDGAR.
	JobKindWatchlist = "watchlist"
	// JobKindForce es el pipeline completo: edgar → prices → sector → metrics →
	// scores sobre el universo (watchlist ∪ securities activas con precio).
	JobKindForce = "force"

	JobStatusIdle    = "idle"
	JobStatusRunning = "running"
	JobStatusDone    = "done"
	JobStatusError   = "error"

	// jobTimeout (D2) acota el job: desligado del request pero no infinito, para
	// que un proveedor (SEC/Yahoo) colgado no se coma la vida del proceso. 30
	// min sobra holgadamente para el universo local.
	jobTimeout = 30 * time.Minute
)

// PipelineStatus es el cuerpo de GET /pipeline/status y del 202 de
// POST /force-refresh. Todas las claves están SIEMPRE presentes (null o vacío
// cuando no aplican) para que el SPA no tenga que distinguir "ausente" de
// "null".
type PipelineStatus struct {
	Status     string         `json:"status"`     // idle|running|done|error
	Kind       *string        `json:"kind"`       // watchlist|force|null
	Tickers    []string       `json:"tickers"`    // [] si no hay
	Steps      map[string]int `json:"steps"`      // {} si no hay
	StartedAt  *time.Time     `json:"started_at"` // null si nunca hubo job
	FinishedAt *time.Time     `json:"finished_at"`
	Error      *string        `json:"error"`
	Pending    []string       `json:"pending"` // cola FIFO (D6), [] si no hay
}

// stepFunc reporta el avance de una etapa del job al runner (misma forma que
// pipeline.StepFunc; tipo propio para no acoplar los tests al paquete pipeline).
type stepFunc func(step string, n int)

// jobFunc es el ejecutor real de un job (runPipelineJob en producción; un fake
// en los tests). onStep puede ser nil.
type jobFunc func(ctx context.Context, pool *pgxpool.Pool, kind string, tickers []string, onStep stepFunc) error

// jobRunner es el único punto de estado global del paquete api (plan B4).
type jobRunner struct {
	mu      sync.Mutex
	pool    *pgxpool.Pool // pool del proceso (el mismo que NewRouter); single-user
	job     *jobState     // nil = nunca hubo job (idle)
	sync    bool          // POST /refresh (síncrono) ocupa el pipeline
	pending []string      // cola FIFO de tickers esperando un job watchlist
	genSeq  int64         // contador de generaciones de job (guard de jobState.gen)
	exec    jobFunc
}

// jobState es la foto de un job. terminal=true significa done|error ya
// publicados: el job sigue consultable (GET /pipeline/status lo devuelve) pero
// ya no ocupa el pipeline.
type jobState struct {
	gen        int64
	kind       string
	tickers    []string
	steps      map[string]int
	startedAt  time.Time
	finishedAt *time.Time
	err        string
	terminal   bool
}

// jobs es el runner del proceso. Los tests de integración lo sustituyen por un
// runner con exec fake (para que un PUT /watchlist de los tests M5 no dispare
// ingesta real contra Yahoo).
var jobs = newJobRunner(runPipelineJob)

func newJobRunner(exec jobFunc) *jobRunner { return &jobRunner{exec: exec} }

// status devuelve el estado consultable del proceso (idle si nunca hubo job,
// o el del último job —incluido su estado terminal— + la cola pendiente).
func (r *jobRunner) status() PipelineStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusLocked()
}

// statusLocked construye el estado SIN releer el lock (lo usan las variantes que
// ya lo tienen, para no relajar y reacondicionar r.mu).
func (r *jobRunner) statusLocked() PipelineStatus {
	// Slices/maps no-nil desde el principio: el contrato "siempre presentes".
	st := PipelineStatus{
		Status:  JobStatusIdle,
		Tickers: []string{},
		Steps:   map[string]int{},
		Pending: append([]string{}, r.pending...),
	}
	if st.Pending == nil {
		st.Pending = []string{}
	}
	if r.job == nil {
		return st
	}
	st.Status = JobStatusRunning
	kind := r.job.kind
	st.Kind = &kind
	st.Tickers = append([]string{}, r.job.tickers...)
	if st.Tickers == nil {
		st.Tickers = []string{}
	}
	for k, v := range r.job.steps {
		st.Steps[k] = v
	}
	started := r.job.startedAt
	st.StartedAt = &started
	if r.job.terminal {
		if r.job.err != "" {
			st.Status = JobStatusError
			e := r.job.err
			st.Error = &e
		} else {
			st.Status = JobStatusDone
		}
		if r.job.finishedAt != nil {
			fin := *r.job.finishedAt
			st.FinishedAt = &fin
		}
	}
	return st
}

// busyLocked indica si el pipeline está ocupado: por el refresh síncrono
// (POST /refresh, D4) o por un job aún no terminal. Un job terminal NO
// bloquea: el siguiente start lo reemplaza.
func (r *jobRunner) busyLocked() bool {
	return r.sync || (r.job != nil && !r.job.terminal)
}

// start arranca un job de kind en segundo plano (POST /force-refresh). Si el
// pipeline está ocupado devuelve (estado actual, false) → el handler responde
// 409 CodeConflict. Si arranca, devuelve (estado inicial, true).
func (r *jobRunner) start(pool *pgxpool.Pool, kind string, tickers []string) (PipelineStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busyLocked() {
		return r.statusLocked(), false
	}
	// Igual que requestIngest: nunca se pisa un pool válido con nil (el job
	// encadenado por la cola necesita el pool aunque arranque desde otro camino).
	if pool != nil {
		r.pool = pool
	}
	st := r.newJobStateLocked(kind, tickers)
	r.job = st
	go r.loop(st)
	return r.statusLocked(), true
}

// requestIngest registra la ingesta en segundo plano de los tickers añadidos a la
// watchlist (PUT /watchlist/{ticker}). NUNCA falla: si el pipeline está libre
// arranca un job watchlist; si está ocupado encola los tickers que no estén ya
// cubiertos (ni por el job en curso —criterio (a) de D1, no repetir trabajo— ni
// por la cola). El handler responde 200 en ambos casos.
func (r *jobRunner) requestIngest(pool *pgxpool.Pool, tickers ...string) PipelineStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	// El pool se fija siempre que se reciba uno: si hay cola y la drainage la
	// dispara un POST /refresh (que no pasa por start), el job encadenado
	// necesita el pool. Nunca se sobrescribe con nil.
	if pool != nil {
		r.pool = pool
	}
	list := normalizeJobTickers(tickers)
	if len(list) == 0 {
		return r.statusLocked()
	}
	if !r.busyLocked() {
		st := r.newJobStateLocked(JobKindWatchlist, list)
		r.job = st
		go r.loop(st)
		return r.statusLocked()
	}
	var current []string
	if r.job != nil {
		current = r.job.tickers
	}
	for _, tk := range list {
		if containsTicker(current, tk) || containsTicker(r.pending, tk) {
			continue
		}
		r.pending = append(r.pending, tk)
	}
	return r.statusLocked()
}

// acquire ocupa el pipeline para el POST /refresh síncrono (D4): false si ya hay
// un job en curso (→ 409). El refresh síncrono NO aparece en PipelineStatus (no
// es una ingesta), pero su lock evita escrituras concurrentes en
// derived_metrics/scores mientras corre el pipeline en segundo plano.
func (r *jobRunner) acquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busyLocked() {
		return false
	}
	r.sync = true
	return true
}

// release libera el refresh síncrono y, si quedó cola pendiente, la drena
// encadenando el job watchlist (si no, una cola podría quedar atrapada detrás
// de un /refresh concurrente).
func (r *jobRunner) release() {
	r.mu.Lock()
	r.sync = false
	next, ok := r.nextLocked()
	r.mu.Unlock()
	if ok {
		go r.loop(next)
	}
}

// newJobStateLocked crea un job con generación nueva. No arranca nada: el
// llamante publica r.job y lanza la goroutine.
func (r *jobRunner) newJobStateLocked(kind string, tickers []string) *jobState {
	r.genSeq++
	return &jobState{
		gen:       r.genSeq,
		kind:      kind,
		tickers:   append([]string{}, tickers...),
		steps:     map[string]int{},
		startedAt: time.Now(),
	}
}

// loop ejecuta el job con contexto PROPIO (D2: la causa raíz del "failed to
// fetch" era atar el pipeline a r.Context(), que el navegador cancelaba al
// navegar), publica el estado terminal y encadena la cola pendiente.
func (r *jobRunner) loop(st *jobState) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	r.mu.Lock()
	pool := r.pool
	r.mu.Unlock()

	started := time.Now()
	slog.Info("pipeline job: inicio", "kind", st.kind, "tickers", len(st.tickers))
	err := r.exec(ctx, pool, st.kind, st.tickers, r.stepReporter(st))

	// D1: la cola se drena como UN job con todos los tickers pendientes, en la
	// misma región crítica que publica el estado terminal.
	next, ok := r.finishAndNext(st, err)
	slog.Info("pipeline job: fin", "kind", st.kind, "tickers", len(st.tickers),
		"status", jobStatusOf(err), "dur_ms", time.Since(started).Milliseconds())
	if ok {
		go r.loop(next)
	}
}

// stepReporter publica steps[step]=n solo mientras st siga siendo el job actual
// (guard de generación: un job viejo no puede pisar el estado del nuevo).
func (r *jobRunner) stepReporter(st *jobState) stepFunc {
	return func(step string, n int) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.job == nil || r.job.gen != st.gen {
			return
		}
		if r.job.steps == nil {
			r.job.steps = map[string]int{}
		}
		r.job.steps[step] = n
	}
}

// finishAndNext publica el estado terminal de st (done|error) con su hora de
// fin y, EN LA MISMA región crítica, drena la cola pendiente.
//
// La atomicidad importa: si el pipeline quedara libre entre publicar el estado
// terminal y el drenaje, un POST /force-refresh o un PUT /watchlist concurrente
// arrancaría un job nuevo y nextLocked lo pisaría al sustituir r.job — el job
// real seguiría ejecutándose sin estado visible y el SPA vería como cancelado un
// force-refresh que el servidor terminó.
//
// Si st ya no es el job actual (un job más nuevo se publicó), no se toca nada.
func (r *jobRunner) finishAndNext(st *jobState, err error) (*jobState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == nil || r.job.gen != st.gen {
		return nil, false
	}
	now := time.Now()
	r.job.finishedAt = &now
	r.job.terminal = true
	if err != nil {
		r.job.err = err.Error()
	}
	return r.nextLocked()
}

// nextLocked construye el job watchlist con TODA la cola (un solo job, no N) y
// reemplaza el job terminal. No encadena si hay un POST /refresh síncrono en
// curso: en ese caso la cola queda intacta y la drena release().
func (r *jobRunner) nextLocked() (*jobState, bool) {
	if len(r.pending) == 0 {
		return nil, false
	}
	if r.sync {
		return nil, false
	}
	tickers := r.pending
	r.pending = nil
	st := r.newJobStateLocked(JobKindWatchlist, tickers)
	r.job = st
	return st, true
}

// runPipelineJob es el ejecutor real de los jobs del proceso.
func runPipelineJob(ctx context.Context, pool *pgxpool.Pool, kind string, tickers []string, onStep stepFunc) error {
	params := LoadParameters()
	ua := os.Getenv("SEC_EDGAR_USER_AGENT")
	if ua == "" {
		ua = refreshUADefault
	}
	report := pipeline.StepFunc(onStep) // tipos nominalmente distintos, mismo underlying
	switch kind {
	case JobKindForce:
		companies := tickers
		if len(companies) == 0 {
			// Universo = watchlist ∪ securities activas con precio (M5.1).
			// Si saliera vacío, ForceRefresh conserva su fallback DefaultCompany.
			universe, err := pipeline.RefreshUniverse(ctx, pool)
			if err != nil {
				return err
			}
			companies = universe
		}
		_, err := pipeline.ForceRefreshWithProgress(ctx, pool, companies, params.GrowthRate, ua, false, report)
		return err
	case JobKindWatchlist:
		_, err := pipeline.WatchlistRefresh(ctx, pool, tickers, params.GrowthRate, false, report)
		return err
	default:
		return fmt.Errorf("pipeline job: kind desconocido %q", kind)
	}
}

// jobStatusOf traduce el resultado del exec al status que se publica.
func jobStatusOf(err error) string {
	if err != nil {
		return JobStatusError
	}
	return JobStatusDone
}

// normalizeJobTickers normaliza (TrimSpace + Upper), descarta vacíos y
// deduplica preservando el orden de entrada (FIFO de la cola).
func normalizeJobTickers(tickers []string) []string {
	out := make([]string, 0, len(tickers))
	seen := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func containsTicker(list []string, ticker string) bool {
	for _, t := range list {
		if t == ticker {
			return true
		}
	}
	return false
}
