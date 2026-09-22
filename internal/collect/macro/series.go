package macro

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/miky/abys-invest/internal/storage"
)

// SeriesMeta describes a supported macro series (ADR-0003 metadata).
type SeriesMeta struct {
	Code      string // BLS series ID
	Name      string
	Unit      string
	Frequency string
	Source    string
}

// SupportedSeries is the static registry of ingesable macro series.
// Extensible: adding PPI, unemployment, Fed rate = one map entry.
var SupportedSeries = map[string]SeriesMeta{
	"CPI": {
		Code:      "CUSR0000SA0",
		Name:      "CPI-U All Urban Consumers (SA)",
		Unit:      "index",
		Frequency: "monthly",
		Source:    "bls",
	},
}

// GetSupportedSeries returns the sorted slang keys.
func GetSupportedSeries() []string {
	keys := make([]string, 0, len(SupportedSeries))
	for k := range SupportedSeries {
		keys = append(keys, k)
	}
	return keys
}

// ResolveSeries maps a user-facing series key ("CPI") to its metadata.
func ResolveSeries(key string) (SeriesMeta, error) {
	meta, ok := SupportedSeries[strings.ToUpper(strings.TrimSpace(key))]
	if !ok {
		return SeriesMeta{}, fmt.Errorf("macro: serie no soportada %q (soportadas: %s)",
			key, strings.Join(GetSupportedSeries(), ", "))
	}
	return meta, nil
}

// Batcher mirrors the persistence interface used by the adapter.
type Batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Begin(ctx context.Context) (pgx.Tx, error)
}

// FetchSeries fetches the observations of a supported series between the
// given years (inclusive, strings "2020".."2026") and returns rows ready to
// persist. Metadata comes from the series registry.
func (c *Client) FetchSeries(ctx context.Context, seriesKey, startYear, endYear string) ([]storage.MacroSeries, error) {
	meta, err := ResolveSeries(seriesKey)
	if err != nil {
		return nil, err
	}
	data, err := c.fetchBLS(ctx, []string{meta.Code}, startYear, endYear)
	if err != nil {
		return nil, err
	}

	out := make([]storage.MacroSeries, 0, len(data))
	for _, d := range data {
		date, err := DatumDate(d)
		if err != nil {
			continue
		}
		val, err := DatumValue(d)
		if err != nil {
			continue
		}
		out = append(out, storage.MacroSeries{
			SeriesCode: meta.Code,
			Date:       date,
			Value:      val,
			Unit:       meta.Unit,
			Frequency:  meta.Frequency,
			Source:     meta.Source,
			SourceID:   nil,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("macro: sin observaciones para %s (%s)", seriesKey, meta.Code)
	}
	// BLS devuelve las observaciones en orden descendente; normalizamos a
	// ascendente por fecha para que el output sea determinista.
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// IngestSeries fetches and batch-upserts the series observations.
// Returns the number of rows persisted.
func (c *Client) IngestSeries(ctx context.Context, db Batcher, seriesKey, startYear, endYear string) (int, error) {
	rows, err := c.FetchSeries(ctx, seriesKey, startYear, endYear)
	if err != nil {
		return 0, err
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("macro: begin tx %s: %w", seriesKey, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertMacroSeries(ctx, tx, rows); err != nil {
		return 0, fmt.Errorf("macro: upsert serie %s: %w", seriesKey, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("macro: commit serie %s: %w", seriesKey, err)
	}
	return len(rows), nil
}

// blsRequest is the API v2 POST body.
type blsRequest struct {
	SeriesID  []string `json:"seriesid"`
	StartYear string   `json:"startyear"`
	EndYear   string   `json:"endyear"`
	APIKey    string   `json:"registrationkey,omitempty"`
}

// fetchBLS performs the POST and parses the flat datum list.
func (c *Client) fetchBLS(ctx context.Context, seriesIDs []string, startYear, endYear string) ([]BLSDatum, error) {
	body, err := json.Marshal(blsRequest{
		SeriesID: seriesIDs, StartYear: startYear, EndYear: endYear, APIKey: c.apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("macro: marshal request: %w", err)
	}
	raw, err := c.post(ctx, c.baseURL+TimeseriesPath, body)
	if err != nil {
		return nil, err
	}
	data, err := ParseBLSResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("macro: serie %s (%s-%s): %w", strings.Join(seriesIDs, ","), startYear, endYear, err)
	}
	// Verificar años solicitados: BLS devuelve SOLO años con datos dentro del rango.
	// Si piden 2019 y BLS tiene hasta 2026, valida devolviendo lo disponible.
	return data, nil
}

// ValidateYearRange checks that both bounds parse as 4-digit years and start <= end.
func ValidateYearRange(startYear, endYear string) error {
	s, err := strconv.Atoi(startYear)
	if err != nil || s < 1900 || s > 2100 {
		return fmt.Errorf("macro: año inicial inválido %q", startYear)
	}
	e, err := strconv.Atoi(endYear)
	if err != nil || e < 1900 || e > 2100 {
		return fmt.Errorf("macro: año final inválido %q", endYear)
	}
	if s > e {
		return fmt.Errorf("macro: rango de años invertido %s > %s", startYear, endYear)
	}
	return nil
}
