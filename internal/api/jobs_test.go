package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tests unitarios del runner de jobs en segundo plano (plan M5.1 T3, package
// api SIN build tag): sin BD, sin red y sin handler — solo el estado en memoria,
// la anti-concurrencia, la cola FIFO y el contrato de contexto (la regresión del
// incidente "failed to fetch": el job NO puede depender del llamador).
//
// Qué cubre cada capa (el ejecutor fake NO es el ejecutor real):
//   - aquí (T3.x, exec fake): el runner — estado, cola FIFO, ctx propio, drenaje;
//   - m51_jobs_integration_test.go (BD de prueba, exec fake): los endpoints
//     reales (GET /pipeline/status, POST /force-refresh, PUT /watchlist) con el
//     runner global cableado a un ejecutor fake, para que ningún test dispare
//     ingesta real contra Yahoo;
//   - runPipelineJob (el ejecutor real, cableado en jobs.go) NO lo ejercita
//     ninguno de los dos: el init() de la suite de integración sustituye
//     `jobs.exec` por un fake en todo el binario de test. Queda cubierto por los
//     asserts F3 del final de este archivo (kind desconocido y pool nil, ambos
//     antes de tocar BD o red) y de extremo a extremo por el smoke en vivo.

// fakeRun es un job que el ejecutor fake ha registrado.
type fakeRun struct {
	kind    string
	tickers []string
}

// fakeExec es un jobFunc controlable: registra cada job, se queda bloqueado
// hasta que el test lo libera y reporta las etapas indicadas.
type fakeExec struct {
	mu       sync.Mutex
	runs     []fakeRun
	arrived  chan struct{} // señal (bufferizada) de "el job ya está corriendo"
	release  chan struct{} // cerrado ⇒ el job continúa
	released sync.Once
	steps    []string // etapas reportadas al terminar
	err      error
	// deadline es el margen del deadline del ctx del job (0 = sin deadline) y
	// ctxErr el error del ctx tal como lo recibió el ejecutor.
	deadline time.Duration
	ctxErr   error
}

func newFakeExec() *fakeExec {
	return &fakeExec{arrived: make(chan struct{}, 8), release: make(chan struct{})}
}

func (f *fakeExec) exec(ctx context.Context, _ *pgxpool.Pool, kind string, tickers []string, onStep stepFunc) error {
	f.mu.Lock()
	f.runs = append(f.runs, fakeRun{kind: kind, tickers: append([]string{}, tickers...)})
	steps := append([]string{}, f.steps...)
	err := f.err
	f.mu.Unlock()

	if dl, ok := ctx.Deadline(); ok {
		f.mu.Lock()
		f.deadline = time.Until(dl)
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
	}
	select {
	case f.arrived <- struct{}{}:
	default:
	}
	<-f.release
	if onStep != nil {
		for _, s := range steps {
			onStep(s, 1)
		}
	}
	return err
}

// releaseJobs desbloquea el ejecutor fake (idempotente).
func (f *fakeExec) releaseJobs() { f.released.Do(func() { close(f.release) }) }

func (f *fakeExec) recorded() []fakeRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRun{}, f.runs...)
}

// waitForJob espera a que el ejecutor fake haya registrado n jobs (con timeout:
// un test que espera en canal sin timeout se cuelga la suite entera).
func (f *fakeExec) waitForJob(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if len(f.recorded()) >= n {
			return
		}
		select {
		case <-f.arrived:
		case <-deadline:
			t.Fatalf("timeout esperando %d job(s) en el ejecutor fake; registrados %d", n, len(f.recorded()))
		}
	}
}

// waitStatus sondea el estado del runner hasta que cond sea cierto.
func waitStatus(t *testing.T, r *jobRunner, what string, cond func(PipelineStatus) bool) PipelineStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := r.status()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando %s; último estado: %+v", what, st)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// T3.1 — estado inicial: idle con TODAS las claves presentes y vacías.
func TestJobRunnerIdleInitial(t *testing.T) {
	r := newJobRunner(newFakeExec().exec)
	st := r.status()
	if st.Status != JobStatusIdle {
		t.Fatalf("estado inicial esperado idle, got %q", st.Status)
	}
	if st.Kind != nil || st.StartedAt != nil || st.FinishedAt != nil || st.Error != nil {
		t.Fatalf("idle no debe tener kind/timestamps/error: %+v", st)
	}
	if st.Tickers == nil || len(st.Tickers) != 0 {
		t.Fatalf("tickers debe ser [] no nil: %+v", st.Tickers)
	}
	if st.Steps == nil || len(st.Steps) != 0 {
		t.Fatalf("steps debe ser {} no nil: %+v", st.Steps)
	}
	if st.Pending == nil || len(st.Pending) != 0 {
		t.Fatalf("pending debe ser [] no nil: %+v", st.Pending)
	}
}

// T3.2 — start publica running con kind, tickers y started_at (sin finished_at).
func TestJobRunnerStartRunning(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, nil); !started {
		t.Fatal("el primer start debe arrancar el job")
	}
	f.waitForJob(t, 1)

	st := r.status()
	if st.Status != JobStatusRunning {
		t.Fatalf("esperado running, got %q", st.Status)
	}
	if st.Kind == nil || *st.Kind != JobKindForce {
		t.Fatalf("kind esperado force, got %+v", st.Kind)
	}
	if st.Tickers == nil || len(st.Tickers) != 0 {
		t.Fatalf("force sin tickers explícitos debe publicar []: %+v", st.Tickers)
	}
	if st.StartedAt == nil {
		t.Fatal("running debe tener started_at")
	}
	if st.FinishedAt != nil {
		t.Fatalf("running no debe tener finished_at: %+v", st.FinishedAt)
	}
	f.releaseJobs()
	waitStatus(t, r, "job done", func(s PipelineStatus) bool { return s.Status == JobStatusDone })
}

// T3.3 — anti-concurrencia: un segundo start falla y NO altera el estado.
func TestJobRunnerStartConflict(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, []string{"AAPL"}); !started {
		t.Fatal("el primer start debe arrancar el job")
	}
	f.waitForJob(t, 1)
	before := r.status()

	st, started := r.start(nil, JobKindForce, nil)
	if started {
		t.Fatal("el segundo start debe fallar (409 en el handler): el pipeline está ocupado")
	}
	after := r.status()
	if after.Status != before.Status || after.Kind == nil || *after.Kind != JobKindForce {
		t.Fatalf("el start en conflicto no debe cambiar el estado: %+v → %+v", before, after)
	}
	if !equalStrings(st.Tickers, before.Tickers) {
		t.Fatalf("el estado devuelto por el start en conflicto debe ser el del job en curso: %+v", st)
	}
	f.releaseJobs()
	waitStatus(t, r, "job done", func(s PipelineStatus) bool { return s.Status == JobStatusDone })

	// Con el job terminal, el pipeline queda libre (no bloquea).
	if _, started := r.start(nil, JobKindWatchlist, []string{"MSFT"}); !started {
		t.Fatal("un job terminal no debe bloquear el siguiente start")
	}
	f.releaseJobs()
	waitStatus(t, r, "segundo job done", func(s PipelineStatus) bool {
		return s.Status == JobStatusDone && s.Kind != nil && *s.Kind == JobKindWatchlist
	})
}

// T3.4 — requestIngest con job en curso: cola FIFO, dedup y criterio (a) (un
// ticker ya cubierto por el job en curso no se encola).
func TestJobRunnerRequestIngestQueues(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, []string{"AAPL"}); !started {
		t.Fatal("el start debe arrancar el job force")
	}
	f.waitForJob(t, 1)

	// "msft" se normaliza a MSFT; "AAPL" ya está en el job en curso (criterio
	// (a): no se encola); "NVDA" repetido se deduplica en la cola.
	st := r.requestIngest(nil, "msft", "AAPL", "NVDA", " nvda ", "")
	if !equalStrings(st.Pending, []string{"MSFT", "NVDA"}) {
		t.Fatalf("cola FIFO con normalización y dedup esperada [MSFT NVDA], got %+v", st.Pending)
	}
	if st.Status != JobStatusRunning {
		t.Fatalf("requestIngest no debe cambiar el estado del job en curso: %+v", st)
	}
	f.releaseJobs()
	waitStatus(t, r, "drenaje de la cola", func(s PipelineStatus) bool {
		return s.Kind != nil && *s.Kind == JobKindWatchlist
	})
}

// T3.5 — drenaje: al terminar el job en curso, el runner encadena UN job
// watchlist con TODOS los tickers pendientes y termina done.
func TestJobRunnerDrainsPendingQueue(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, []string{"AAPL"}); !started {
		t.Fatal("el start debe arrancar el job force")
	}
	f.waitForJob(t, 1)
	r.requestIngest(nil, "MSFT", "NVDA")
	f.releaseJobs()

	final := waitStatus(t, r, "segundo job watchlist done", func(s PipelineStatus) bool {
		return s.Kind != nil && *s.Kind == JobKindWatchlist && s.Status == JobStatusDone
	})
	if !equalStrings(final.Tickers, []string{"MSFT", "NVDA"}) {
		t.Fatalf("el job drenado debe llevar todos los pendientes, got %+v", final.Tickers)
	}
	if len(final.Pending) != 0 {
		t.Fatalf("la cola debe quedar vacía tras el drenaje: %+v", final.Pending)
	}
	runs := f.recorded()
	if len(runs) != 2 {
		t.Fatalf("se esperaban 2 jobs (force + watchlist), hubo %d: %+v", len(runs), runs)
	}
	if runs[0].kind != JobKindForce || !equalStrings(runs[0].tickers, []string{"AAPL"}) {
		t.Fatalf("orden de ejecución incorrecto en el primer job: %+v", runs[0])
	}
	if runs[1].kind != JobKindWatchlist || !equalStrings(runs[1].tickers, []string{"MSFT", "NVDA"}) {
		t.Fatalf("orden de ejecución incorrecto en el job drenado: %+v", runs[1])
	}
}

// T3.6 — job con error: status error, mensaje no vacío y finished_at publicado.
func TestJobRunnerJobError(t *testing.T) {
	f := newFakeExec()
	f.err = context.DeadlineExceeded
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, nil); !started {
		t.Fatal("el start debe arrancar el job")
	}
	f.releaseJobs()
	f.waitForJob(t, 1)

	st := waitStatus(t, r, "job error", func(s PipelineStatus) bool { return s.Status == JobStatusError })
	if st.Error == nil || *st.Error == "" {
		t.Fatalf("un job fallido debe publicar el error: %+v", st)
	}
	if st.FinishedAt == nil {
		t.Fatal("un job fallido debe publicar finished_at")
	}
	if st.StartedAt == nil {
		t.Fatal("un job fallido debe conservar started_at")
	}
}

// T3.7 — REGRESIÓN DEL INCIDENTE: el contexto del job no depende del llamador.
// El job se lanza con context.Background() + timeout propio (D2): cancelar un
// contexto externo (o simplemente no tenerlo) no lo aborta, y su ctx trae
// deadline ≈ jobTimeout en vez de vivir eternamente.
func TestJobRunnerContextIndependentFromCaller(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)

	// Contexto "del cliente" que se cancela de inmediato, como el r.Context()
	// de un POST /force-refresh cuyo navegador navega (el runner nunca lo
	// recibe: usa context.Background(), D2).
	_, cancelCaller := context.WithCancel(context.Background())

	if _, started := r.start(nil, JobKindForce, nil); !started {
		t.Fatal("el start debe arrancar el job")
	}
	cancelCaller() // el "navegar/recargar" del navegador
	f.waitForJob(t, 1)

	// Con el cliente ya cancelado, el job sigue vivo (bloqueado en el fake).
	if st := r.status(); st.Status != JobStatusRunning {
		t.Fatalf("cancelar el contexto del cliente no debe abortar el job: %+v", st)
	}
	f.releaseJobs()
	waitStatus(t, r, "job done pese al cliente cancelado", func(s PipelineStatus) bool { return s.Status == JobStatusDone })

	f.mu.Lock()
	deadline, ctxErr := f.deadline, f.ctxErr
	f.mu.Unlock()
	if ctxErr != nil {
		t.Fatalf("el ctx del job no debe estar cancelado: %v", ctxErr)
	}
	if deadline <= 0 {
		t.Fatalf("el ctx del job debe tener deadline propio, margen=%v", deadline)
	}
	if deadline > jobTimeout || deadline < jobTimeout-time.Minute {
		t.Fatalf("el deadline debe ser ≈jobTimeout (%v), margen=%v", jobTimeout, deadline)
	}
	if !strings.Contains(jobStatusOf(nil), JobStatusDone) {
		t.Fatalf("jobStatusOf(nil) debe ser done, got %q", jobStatusOf(nil))
	}
	if jobStatusOf(context.Canceled) != JobStatusError {
		t.Fatalf("jobStatusOf(err) debe ser error, got %q", jobStatusOf(context.Canceled))
	}
}

// T3.8 — el drenaje de la cola es ATÓMICO con la publicación del estado
// terminal. Si entre ambas cosas el pipeline quedara libre, un POST
// /force-refresh concurrente arrancaría un job que el drenaje pisaría: el job
// real seguiría corriendo sin estado visible. Se comprueba la consecuencia
// observable: al volver de finishAndNext el job encadenado ya está publicado y
// ocupa el pipeline, así que un start concurrente recibe 409 en vez de pisarlo.
func TestJobRunnerDrainIsAtomicWithTerminalState(t *testing.T) {
	f := newFakeExec()
	r := newJobRunner(f.exec)
	if _, started := r.start(nil, JobKindForce, []string{"AAPL"}); !started {
		t.Fatal("el start debe arrancar el job force")
	}
	f.waitForJob(t, 1)
	r.requestIngest(nil, "MSFT") // cola pendiente

	// finishAndNext hace las dos cosas bajo el mismo lock (loop no se usa aquí
	// para poder observar el estado entre medias).
	r.mu.Lock()
	current := r.job
	r.mu.Unlock()
	next, ok := r.finishAndNext(current, nil)
	if !ok {
		t.Fatal("finishAndNext debe encadenar el job con la cola pendiente")
	}
	if !equalStrings(next.tickers, []string{"MSFT"}) {
		t.Fatalf("el job encadenado debe llevar la cola completa, got %+v", next.tickers)
	}
	// El job encadenado ya está publicado y NO terminal: ocupa el pipeline.
	st := r.status()
	if st.Status != JobStatusRunning || st.Kind == nil || *st.Kind != JobKindWatchlist {
		t.Fatalf("tras el drenaje el estado debe ser el del job watchlist: %+v", st)
	}
	if _, started := r.start(nil, JobKindForce, []string{"NVDA"}); started {
		t.Fatal("un start concurrente al drenaje debe obtener 409, no pisar el job encolado")
	}

	// Encadenado como hace loop; se libera el ejecutor fake para que el job
	// drenado termine de inmediato.
	f.releaseJobs()
	go r.loop(next)
	final := waitStatus(t, r, "job drenado done", func(s PipelineStatus) bool { return s.Status == JobStatusDone })
	if !equalStrings(final.Tickers, []string{"MSFT"}) {
		t.Fatalf("estado final inesperado: %+v", final)
	}
}

// --- F3 (REVIEW de abys-m51-watchlist-integration): el EJECUTOR REAL ---------
//
// El runner se prueba en T3.x y en m51_jobs_integration_test.go siempre con un
// exec fake (el init() de la suite de integración sustituye `jobs.exec` en todo
// el binario de test), así que la lógica propia de runPipelineJob — el switch de
// kind y el cableado kind → pipeline — solo se ejercitaba en el smoke en vivo.
// Estos dos asserts unitarios (sin BD, sin red, sin panic) cierran ese hueco:
// cada caso aborta en el switch de kind o en el primer guard del pipeline, luego
// no puede haber tocado la BD ni un proveedor.

// F3.1 — force sin tickers con pool nil (API levantado sin BD): el ejecutor
// resuelve el universo con pipeline.RefreshUniverse, que aborta en su guard de
// pool. El error debe ser explícito ("pool nil") y volver de inmediato: si
// tocara la red o la BD no lo haría, y el mensaje tampoco sería el del guard.
func TestRunPipelineJobForceNilPool(t *testing.T) {
	start := time.Now()
	err := runPipelineJob(context.Background(), nil, JobKindForce, nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runPipelineJob(force, pool nil) debe fallar: el pipeline exige pool no nil")
	}
	if !strings.Contains(err.Error(), "pool nil") {
		t.Fatalf("se esperaba el error de pool nil del guard de RefreshUniverse, got %v", err)
	}
	// Margen generoso (una llamada que aborta en el primer guard tarda µs): solo
	// busca detectar un intento de red/BD, no medir rendimiento.
	if elapsed > 2*time.Second {
		t.Fatalf("el fallo por pool nil debe ser inmediato (sin red ni consulta), tardó %v", elapsed)
	}
}

// F3.2 — kind desconocido: el switch lo rechaza con un error claro que nombra el
// kind, sin mirar el pool y sin panic. El kind se decide ANTES que el pool: por
// eso el error NO puede ser el de pool nil (si lo fuera, este assert no
// distinguiría el switch de un simple guard de pool).
func TestRunPipelineJobUnknownKind(t *testing.T) {
	const kind = "otro"
	err := runPipelineJob(context.Background(), nil, kind, nil, nil)
	if err == nil {
		t.Fatalf("runPipelineJob(kind=%q) debe fallar: kind no previsto", kind)
	}
	if !strings.Contains(err.Error(), "kind desconocido") || !strings.Contains(err.Error(), kind) {
		t.Fatalf("el error debe identificar el kind desconocido %q, got %v", kind, err)
	}
	if strings.Contains(err.Error(), "pool nil") {
		t.Fatalf("un kind desconocido debe rechazarse antes de mirar el pool, got %v", err)
	}
}
