package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// normalizeTicker uppercases and trims a path/query ticker, rejecting empty
// or over-long values.
func normalizeTicker(raw string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(raw))
	if t == "" {
		return "", errValidation("ticker vacío")
	}
	if len(t) > 16 {
		return "", errValidation("ticker inválido: demasiado largo")
	}
	for _, r := range t {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '-' && r != '=' {
			return "", errValidationf("ticker inválido: %q", raw)
		}
	}
	return t, nil
}

// errValidation is a typed validation result used by parse helpers.
type errValidation string

func (e errValidation) Error() string { return string(e) }
func errValidationf(format string, args ...any) error {
	return errValidation(fmt.Sprintf(format, args...))
}

// tickerListParam parses a CSV tickers query parameter, uppercasing and
// trimming each entry in the order provided (used by the multi-ticker
// endpoints /compare?tickers= y /backtest?tickers=).
func tickerListParam(q url.Values, key string) ([]string, error) {
	raw := strings.TrimSpace(q.Get(key))
	if raw == "" {
		return nil, errValidationf("'%s' es requerido (CSV de tickers)", key)
	}
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return nil, errValidationf("'%s' requiere al menos un ticker", key)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t, err := normalizeTicker(p)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// dateOrEmpty formats a time as YYYY-MM-DD, or "" when zero.
func dateOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// parseIntQuery parses a positive integer query parameter with a default;
// invalid values fail into bad_request.
func parseIntQuery(raw string, def int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 {
		return 0, errValidationf("parámetro numérico inválido: %q", raw)
	}
	return v, nil
}

// parseFloatQuery parses a non-negative float query parameter with a default.
func parseFloatQuery(raw string, def float64) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v < 0 {
		return 0, errValidationf("parámetro numérico inválido: %q", raw)
	}
	return v, nil
}
