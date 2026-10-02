package yahoo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/finviz"
	"github.com/miky/abys-invest/internal/storage"
)

// ReferenceData is the reference data resolved from any source. Beta is
// nil when the source does not provide it: only Yahoo does (plan D17), Finviz
// is a sector/industry fallback and its scraping is NOT used for beta (ToS).
type ReferenceData struct {
	Sector   *string
	Industry *string
	Beta     *float64
}

// EnrichReferenceData resolves sector/industry and beta for each ticker (Yahoo
// quoteSummary primary, Finviz fallback for sector/industry only; plan D1 + B3 +
// D17) and persists them in securities.sector/industry/beta. It returns the
// number of tickers successfully enriched; individual failures are logged,
// never fatal.
func EnrichReferenceData(ctx context.Context, pool *pgxpool.Pool, tickers []string) (int, error) {
	yc := NewClient()
	if ua := os.Getenv("SEC_EDGAR_USER_AGENT"); ua != "" {
		yc = NewClient(WithUserAgent(ua))
	}
	return enrichReferenceData(ctx, pool, tickers, yc, finviz.NewClient())
}

// enrichReferenceData is the dependency-injectable implementation: the sector
// source is the sectorFallback interface (Finviz in production, a fake in
// tests) so the whole resolution chain runs without network.
func enrichReferenceData(ctx context.Context, pool *pgxpool.Pool, tickers []string, yc *Client, fz sectorFallback) (int, error) {
	if len(tickers) == 0 {
		return 0, nil
	}
	slog.Info("enriquecimiento de referencia", "tickers", len(tickers),
		"fuente_primaria", "yahoo quoteSummary (assetProfile+defaultKeyStatistics)", "fallback", "finviz (solo sector)")
	updated := 0
	for _, raw := range tickers {
		ticker := strings.ToUpper(strings.TrimSpace(raw))
		if ticker == "" {
			continue
		}
		ref, err := resolveReferenceData(ctx, ticker, yc, fz)
		if err != nil {
			slog.Warn("referencia no resoluble (queda NULL; comparables degradan)", "ticker", ticker, "error", err)
			continue
		}
		if err := storage.UpdateSecurityReference(ctx, pool, ticker, ref.Sector, ref.Industry, ref.Beta); err != nil {
			slog.Warn("persistir referencia falló", "ticker", ticker, "error", err)
			continue
		}
		updated++
		slog.Info("referencia actualizada", "ticker", ticker,
			"sector", deref(ref.Sector), "industry", deref(ref.Industry), "beta", derefFloat(ref.Beta))
	}
	return updated, nil
}

// sectorFallback abstracts the non-primary sector source (Finviz) so
// resolveReferenceData is testable without network. It provides sector/industry
// only: no beta (scraping Finviz for beta is not used, plan D17).
type sectorFallback interface {
	GetSectorInfo(ctx context.Context, ticker string) (*finviz.SectorInfo, error)
}

// resolveReferenceData tries Yahoo quoteSummary first (sector AND beta from the
// same response) and Finviz as fallback for the sector.
//
// The beta does NOT depend on the sector: a response with defaultKeyStatistics
// but without assetProfile still persists the beta, with the sector resolved by
// Finviz. That independence is why the beta is a column of the catalog and not
// a byproduct of the sector.
func resolveReferenceData(ctx context.Context, ticker string, yc *Client, fz sectorFallback) (*ReferenceData, error) {
	var (
		ySector, yIndustry string
		beta               *float64
		yahooOK            bool
	)
	if qs, err := yc.GetQuoteSummary(ctx, ticker); err == nil && len(qs.QuoteSummary.Result) > 0 {
		yahooOK = true
		ap := qs.QuoteSummary.Result[0].AssetProfile
		ySector, yIndustry = ap.Sector, ap.Industry
		beta, _ = KeyStatsFor(qs)
	} else if err != nil {
		slog.Debug("yahoo quoteSummary no disponible, probando finviz", "ticker", ticker, "error", err)
	}

	if ySector != "" || yIndustry != "" {
		return &ReferenceData{Sector: optional(ySector), Industry: optional(yIndustry), Beta: beta}, nil
	}

	// Sin sector en Yahoo (respuesta vacía, sin assetProfile o sin sector): el
	// sector degrada a Finviz. La beta observada es independiente y se conserva
	// aunque Finviz también falle.
	if yahooOK {
		slog.Debug("yahoo sin sector, probando finviz", "ticker", ticker)
	}
	if fi, err := fz.GetSectorInfo(ctx, ticker); err == nil {
		if fi.Sector == "" && fi.Industry == "" {
			if beta == nil {
				return nil, fmt.Errorf("ni yahoo ni finviz devuelven sector para %s", ticker)
			}
			return &ReferenceData{Beta: beta}, nil
		}
		// Finviz no aporta beta: aquí la referencia queda sin beta observada y el
		// WACC degradará a configured_fallback de forma explícita.
		return &ReferenceData{Sector: optional(fi.Sector), Industry: optional(fi.Industry), Beta: beta}, nil
	} else {
		if beta != nil {
			slog.Warn("beta observada sin sector ni finviz; se persiste solo la beta", "ticker", ticker, "error", err)
			return &ReferenceData{Beta: beta}, nil
		}
		return nil, fmt.Errorf("finviz fallback falló para %s: %w", ticker, err)
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.4f", *v)
}
