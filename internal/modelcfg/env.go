package modelcfg

import (
	"log/slog"
	"math"
	"os"
	"strconv"
)

// Shared, VALIDATING env helpers (plan M6c B1/B16, ADR D20, closes M6b-H2).
//
// They replace the four private copies that lived in internal/growth,
// internal/wacc and internal/valuation (and the two of internal/pipeline) with
// ONE implementation. The contract of every helper is the same:
//
//   - missing or empty          → the default, silently (nothing to complain about)
//   - unparseable               → default + slog.Warn
//   - NaN / ±Inf                → default + slog.Warn
//   - out of the accepted range → default + slog.Warn (EnvFloatRange/EnvIntRange)
//
// Two rules make these helpers *validators* and not merely readers:
//
//  1. A bad env var must NEVER break the pipeline. There is no error return on
//     purpose: a knob is not worth aborting a job for.
//  2. A bad env var must never poison a formula either. A NaN that reaches a
//     ratio propagates into the score, into the persisted row and into the API,
//     and by then nobody can tell it came from the environment.
//
// The helpers log the offending VALUE because every variable they read is a
// numeric model knob, never a secret; the DSN (which IS a secret) has its own
// redacting reader in internal/testsupport/dsn.go (ADR D30).

// EnvFloat reads a float env var over def. Unparseable, NaN and ±Inf values
// fall back to def with a warning.
func EnvFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("modelcfg: env float inválido, se usa el default",
			"env", key, "valor", v, "default", def)
		return def
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		slog.Warn("modelcfg: env float no finito, se usa el default",
			"env", key, "valor", v, "default", def)
		return def
	}
	return f
}

// EnvFloatRange is EnvFloat plus an inclusive [lo, hi] acceptance range. A value
// outside the range is a configuration mistake (a coverage of 4.2, a negative
// margin of safety, a WACC spread of -80), so it is refused in favour of the
// default instead of being silently accepted.
func EnvFloatRange(key string, def, lo, hi float64) float64 {
	f := EnvFloat(key, def)
	if f < lo || f > hi {
		slog.Warn("modelcfg: env float fuera de rango, se usa el default",
			"env", key, "valor", f, "min", lo, "max", hi, "default", def)
		return def
	}
	return f
}

// EnvInt reads an int env var over def. Unparseable values fall back to def with
// a warning.
func EnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("modelcfg: env int inválido, se usa el default",
			"env", key, "valor", v, "default", def)
		return def
	}
	return i
}

// EnvIntRange is EnvInt plus an inclusive [lo, hi] acceptance range.
func EnvIntRange(key string, def, lo, hi int) int {
	i := EnvInt(key, def)
	if i < lo || i > hi {
		slog.Warn("modelcfg: env int fuera de rango, se usa el default",
			"env", key, "valor", i, "min", lo, "max", hi, "default", def)
		return def
	}
	return i
}

// EnvBool reads a 1/true/yes/on flag. Anything else (including a typo such as
// "True1") falls back to def with a warning, so a half-typed flag can never flip
// a model decision in an unexpected direction.
func EnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "on":
		return true
	case "0", "false", "FALSE", "False", "no", "NO", "off":
		return false
	}
	slog.Warn("modelcfg: env booleano inválido, se usa el default",
		"env", key, "valor", v, "default", def)
	return def
}
