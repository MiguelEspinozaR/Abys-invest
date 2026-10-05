package finviz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const quotePageFixture = `<!DOCTYPE html>
<html><head><title>AAPL Stock Quote</title></head><body>
<table>
<tr><td class="fullview-links">
<a class="tab-link" href="news.ashx">News</a>
</td></tr>
</table>
<table>
<tr>
<td class="snapshot-table2">
<div class="quote-header_categories">
<a href="screener?v=111&amp;f=sec_technology" class="quote-header_category">Technology</a>
<a href="screener?v=111&amp;f=ind_consumerelectronics" class="quote-header_category" title="Consumer Electronics">Consumer Electronics</a>
</div>
</td>
</tr>
</table>
</body></html>`

func TestParseQuotePage(t *testing.T) {
	info, err := parseQuotePage([]byte(quotePageFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Sector != "Technology" {
		t.Fatalf("sector esperado %q, got %q", "Technology", info.Sector)
	}
	if info.Industry != "Consumer Electronics" {
		t.Fatalf("industry esperado %q, got %q", "Consumer Electronics", info.Industry)
	}
}

func TestParseQuotePageMissing(t *testing.T) {
	if _, err := parseQuotePage([]byte("<html><body>sin categorías</body></html>")); !errors.Is(err, ErrNoSectorInfo) {
		t.Fatalf("se esperaba ErrNoSectorInfo, got %v", err)
	}
}

// categoriesPageWith arma una quote page mínima con el mismo bloque de
// categorías que quotePageFixture: el andamiaje queda fijo (estructura
// table/td/div, href con f=sec_*/f=ind_*, class) y sólo se parametriza el HTML
// que Finviz devuelve. Mantiene además la asimetría de la que dependen las dos
// regex — sectorLinkRe saca el TEXTO del link e industryLinkRe el atributo
// title="", por eso el texto visible del segundo anchor es la constante "ind" y
// su nombre va en el title—. La tabla de casos de abajo declara sólo lo que
// varía y no repite 7 veces ese andamiaje.
func categoriesPageWith(sectorText, industryTitle string) string {
	return `<html><body><table><tr><td class="snapshot-table2"><div class="quote-header_categories">` +
		`<a href="screener?v=111&amp;f=sec_technology" class="quote-header_category">` + sectorText + `</a>` +
		`<a href="screener?v=111&amp;f=ind_oilgas" class="quote-header_category" title="` + industryTitle + `">ind</a>` +
		`</div></td></tr></table></body></html>`
}

// TestParseQuotePageUnescape fija el texto plano exacto que se persiste. Sin
// html.UnescapeString, sector/industry quedaban con literales HTML ("Oil &amp;
// Gas E&P") y esa etiqueta era la que devolvía la API y la que se veía en la
// UI. Cada caso compara la cadena completa, no un Contains: fija también que no
// queden espacios raros ni entidades parciales.
func TestParseQuotePageUnescape(t *testing.T) {
	cases := []struct {
		name         string
		sectorHTML   string
		industryHTML string
		wantSector   string
		wantIndustry string
	}{
		{
			// El caso real de dev: 11 de 44 filas (el fallback se activó cuando
			// Yahoo devolvió 429) quedaron con la entidad dentro del literal.
			name:         "ampersand escapado en industria",
			sectorHTML:   "Energy",
			industryHTML: "Oil &amp; Gas E&amp;P",
			wantSector:   "Energy",
			wantIndustry: "Oil & Gas E&P",
		},
		{
			name:         "entidad de apostrophe en industria",
			sectorHTML:   "Industrials",
			industryHTML: "Producer&#39;s Goods",
			wantSector:   "Industrials",
			wantIndustry: "Producer's Goods",
		},
		{
			// sector no venía afectado en la muestra de dev, pero el fix es del
			// contrato del parser: si mañana Finviz escapa también el sector, la
			// fila persiste texto plano igual.
			name:         "ampersand escapado en sector",
			sectorHTML:   "Consumer &amp; Retail",
			industryHTML: "Internet Retail",
			wantSector:   "Consumer & Retail",
			wantIndustry: "Internet Retail",
		},
		{
			// &nbsp; se convierte en U+00A0 y TrimSpace (unicode.IsSpace) lo
			// recorta; por eso el des-escape va antes del recorte.
			name:         "nbsp colapsado al recortar",
			sectorHTML:   "Energy",
			industryHTML: "Oil &amp; Gas E&amp;P&nbsp;",
			wantSector:   "Energy",
			wantIndustry: "Oil & Gas E&P",
		},
		{
			name:         "comillas y angulos escapados",
			sectorHTML:   "Technology",
			industryHTML: `Cotton &amp; Wool &lt;Textiles&gt; &quot;Home&quot; &apos;A&apos;`,
			wantSector:   "Technology",
			wantIndustry: `Cotton & Wool <Textiles> "Home" 'A'`,
		},
		{
			// El des-escape es de UN nivel (igual que html.UnescapeString): un
			// doble escapado hipotético no se reescanea en la misma pasada. Este
			// caso fija ese contrato, que es el mismo que aplica la migración 018
			// con &amp; al final de la cadena de replace().
			name:         "doble escapado se decodifica en un solo nivel",
			sectorHTML:   "Energy",
			industryHTML: "Oil &amp;#39; Gas",
			wantSector:   "Energy",
			wantIndustry: "Oil &#39; Gas",
		},
		{
			// Asimetría 1/3 con la migración 018: `&#x26;` es la misma "&" en
			// notación hexadecimal. html.UnescapeString la decodifica, la 018 no
			// (su WHERE exige `;` y su cadena no la nombra). No aparece en el
			// dato medido; por eso queda fuera a propósito (§8.2 del plan).
			name:         "entidad hexadecimal",
			sectorHTML:   "Energy",
			industryHTML: "Internet Content &#x26; Information",
			wantSector:   "Energy",
			wantIndustry: "Internet Content & Information",
		},
		{
			// Asimetría 2/3: `&amp` SIN punto y coma también la decodifica Go;
			// la 018 no, y el WHERE tampoco la puede detectar porque exige `;`.
			// Mismo criterio: forma no observada en dev, fuera a propósito
			// (§8.2 del plan).
			name:         "ampersand sin punto y coma",
			sectorHTML:   "Energy",
			industryHTML: "Oil &amp Gas E&P",
			wantSector:   "Energy",
			wantIndustry: "Oil & Gas E&P",
		},
		{
			// Asimetría 3/3: `&AMP;` en mayúsculas la decodifica Go (su tabla de
			// entidades es case-insensitive) y la 018 no (replace() y el `~` del
			// WHERE son case-sensitive en Postgres). Fuera a propósito por el
			// mismo motivo (§8.2 del plan).
			name:         "entidad en mayusculas",
			sectorHTML:   "Technology",
			industryHTML: "Household &AMP; Personal Products",
			wantSector:   "Technology",
			wantIndustry: "Household & Personal Products",
		},
		{
			// Idempotencia del parser: HTML ya plano pasa sin cambios.
			name:         "texto ya plano no cambia",
			sectorHTML:   "Technology",
			industryHTML: "Consumer Electronics",
			wantSector:   "Technology",
			wantIndustry: "Consumer Electronics",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseQuotePage([]byte(categoriesPageWith(tc.sectorHTML, tc.industryHTML)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if info.Sector != tc.wantSector {
				t.Errorf("sector esperado %q, got %q", tc.wantSector, info.Sector)
			}
			if info.Industry != tc.wantIndustry {
				t.Errorf("industry esperado %q, got %q", tc.wantIndustry, info.Industry)
			}
		})
	}
}

// TestParseQuotePageCategoriesSinEnlaces cubre la otra rama de ErrNoSectorInfo:
// el bloque de categorías existe pero no aporta ningún link (Finviz cambió el
// markup o la página came vacía). El contrato no se estrecha con el fix.
func TestParseQuotePageCategoriesSinEnlaces(t *testing.T) {
	page := `<html><body><div class="quote-header_categories"></div></body></html>`
	if _, err := parseQuotePage([]byte(page)); !errors.Is(err, ErrNoSectorInfo) {
		t.Fatalf("se esperaba ErrNoSectorInfo, got %v", err)
	}
}

func TestGetSectorInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "AAPL" {
			http.Error(w, "ticker no enviado", http.StatusBadRequest)
			return
		}
		w.Write([]byte(quotePageFixture))
	}))
	defer srv.Close()

	c := NewClient(WithQuoteURL(srv.URL))
	info, err := c.GetSectorInfo(context.Background(), "aapl")
	if err != nil {
		t.Fatalf("GetSectorInfo: %v", err)
	}
	if info.Sector != "Technology" || !strings.Contains(info.Industry, "Consumer") {
		t.Fatalf("info incorrecto: %+v", info)
	}
}

func TestGetSectorInfoHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(WithQuoteURL(srv.URL))
	if _, err := c.GetSectorInfo(context.Background(), "AAPL"); err == nil {
		t.Fatal("se esperaba error HTTP")
	}
}
