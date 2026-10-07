// Tests del CLI del collector para el camino de RE-INGESTA (plan M6c-T1 W3).
//
// Sin red y sin base de datos: lo que se comprueba aquí son las REGLAS del
// comando (qué combinaciones de flags tienen sentido y qué lista de empresas
// se arma), que es exactamente la parte que un test de integración no puede
// afirmar. El SELECT del universo y la idempotencia de la re-ingesta se
// comprueban en la suite de integración (internal/storage y internal/pipeline).
package main

import (
	"context"
	"strings"
	"testing"
)

func TestValidateEdgarFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   edgarFlags
		wantErr string // "" = válido
	}{
		{
			name:  "edgar sin flags nuevos conserva el contrato histórico",
			flags: edgarFlags{job: jobEdgar},
		},
		{
			name:  "fresh con edgar es el camino de re-ingesta",
			flags: edgarFlags{job: jobEdgar, fresh: true},
		},
		{
			name:  "fresh con all también (all incluye edgar)",
			flags: edgarFlags{job: jobAll, fresh: true},
		},
		{
			name:  "universe staged con edgar es válido",
			flags: edgarFlags{job: jobEdgar, universe: universeStaged},
		},
		{
			name:  "job universe con selector es válido",
			flags: edgarFlags{job: jobUniverse, universe: universeStaged},
		},
		{
			// -dry-run no escribe nada: sin el borrado de staging el -fresh
			// sería un no-op silencioso, que es justo lo que no puede ser.
			name:    "fresh + dry-run se rechaza",
			flags:   edgarFlags{job: jobEdgar, fresh: true, dryRun: true},
			wantErr: "incompatibles",
		},
		{
			// Ambas listas son "empresas a ingerir": aceptar las dos y elegir
			// una en silencio haría que el operador creyera que re-ingestó un
			// universo cuando ingirió otro.
			name:    "companies + universe se rechaza",
			flags:   edgarFlags{job: jobEdgar, companies: []string{"NVDA"}, universe: universeStaged},
			wantErr: "excluyentes",
		},
		{
			name:    "fresh en un job que no canoniza se rechaza",
			flags:   edgarFlags{job: jobPrices, fresh: true},
			wantErr: "sólo aplican a -job edgar|all",
		},
		{
			name:    "universe en un job que no canoniza se rechaza",
			flags:   edgarFlags{job: jobSector, universe: universeStaged},
			wantErr: "sólo aplican a -job edgar|all",
		},
		{
			name:    "job universe sin selector se rechaza",
			flags:   edgarFlags{job: jobUniverse},
			wantErr: "requiere -universe",
		},
		{
			name:    "job universe con fresh se rechaza (sólo lista)",
			flags:   edgarFlags{job: jobUniverse, universe: universeStaged, fresh: true},
			wantErr: "no aplica a -job universe",
		},
		{
			// Sin esta regla el selector fallaría más tarde con un error de DSN
			// (el dry-run de edgar corre sin BD), que no explica la causa.
			name:    "dry-run + universe se rechaza (el selector necesita BD)",
			flags:   edgarFlags{job: jobEdgar, universe: universeStaged, dryRun: true},
			wantErr: "se resuelve contra la BD",
		},
		{
			name:    "job universe con dry-run se rechaza (sólo lista)",
			flags:   edgarFlags{job: jobUniverse, universe: universeStaged, dryRun: true},
			wantErr: "no aplica a -job universe",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEdgarFlags(tc.flags)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateEdgarFlags: error inesperado: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateEdgarFlags: se esperaba error con %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateEdgarFlags: error %q no menciona %q", err, tc.wantErr)
			}
		})
	}
}

func TestEdgarTargets(t *testing.T) {
	ciks := []string{"0000320193", "0000789019"}

	// Sin -companies y sin -universe: nil, y el DefaultCompany histórico sigue
	// aplicándose dentro del pipeline (no se cambia el contrato).
	if got := edgarTargets(edgarFlags{job: jobEdgar}, nil); got != nil {
		t.Fatalf("sin flags la lista debe quedar vacía (nil) para que aplique DefaultCompany, got %v", got)
	}

	// Con -companies: la lista explícita manda (aunque el resolver devolviera
	// algo; en producción son excluyentes por validación).
	got := edgarTargets(edgarFlags{job: jobEdgar, companies: []string{"NVDA", "WMT"}}, ciks)
	if len(got) != 2 || got[0] != "NVDA" || got[1] != "WMT" {
		t.Fatalf("con -companies debe mandar la lista explícita, got %v", got)
	}

	// Con -universe: la lista resuelta del selector.
	got = edgarTargets(edgarFlags{job: jobEdgar, universe: universeStaged}, ciks)
	if len(got) != 2 || got[0] != "0000320193" || got[1] != "0000789019" {
		t.Fatalf("con -universe debe usarse el universo resuelto, got %v", got)
	}
}

// TestResolveUniverseRejectsUnknownSelector: un selector desconocido es un
// error, nunca un "cae al universo por defecto". Un universo distinto del que
// el operador cree es peor que un fallo ruidoso: en la re-ingesta el efecto no
// se ve en los datos (simplemente no se canoniza lo nuevo).
func TestResolveUniverseRejectsUnknownSelector(t *testing.T) {
	_, err := resolveUniverse(context.Background(), nil, "securities")
	if err == nil {
		t.Fatal("resolveUniverse: un selector desconocido debe fallar, no caer por defecto")
	}
	if !strings.Contains(err.Error(), "desconocido") || !strings.Contains(err.Error(), universeStaged) {
		t.Fatalf("el error debe nombrar el selector rechazado y el soportado, got %q", err)
	}
}

// TestResolveUniverseStagedNeedsDB: con el único selector soportado pero sin
// pool el error lo dice, en vez de re-canonizar el DefaultCompany sin que nadie
// lo pidiera. Es la última línea de defensa: la validación ya rechaza
// `-dry-run -universe` (el caso real que llegaba aquí sin pool), pero el selector
// no debe depender de que el caller se acuerde de validarlo.
func TestResolveUniverseStagedNeedsDB(t *testing.T) {
	_, err := resolveUniverse(context.Background(), nil, universeStaged)
	if err == nil {
		t.Fatal("resolveUniverse(staged) sin pool debe fallar")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("el error debe mencionar DATABASE_URL, got %q", err)
	}
}
