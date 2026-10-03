// Package testsupport holds helpers shared by the integration suites (`-tags=integration`).
//
// It exists because of ADR D29 (Az8): beta_history is now the CANONICAL source of
// beta and securities.beta only its cache. Tests that want a CAPM to work must
// therefore seed the OBSERVATION, dated at the value date they are testing — not
// just the cache. Seeding the cache alone silently degrades the WACC to
// configured_fallback, and a test that only checked "beta_observed == true" would
// have kept passing while testing nothing.
//
// ADR D30: DSN redacting helper for safe logging.
package testsupport

import (
	"net/url"
	"regexp"
	"strings"
)

// RedactDSN returns a redacted version of a PostgreSQL DSN, replacing the
// password (and username if present) with ***. It handles both URL and
// key=value formats. Uses net/url for robust URL parsing (handles passwords
// with special chars like /, :, @, etc.).
//
// Examples:
//
//	postgres://user:pass@host:5432/db -> postgres://***@host:5432/db
//	postgres://user:pass/word@host:5432/db -> postgres://***@host:5432/db
//	postgres://user@host:5432/db -> postgres://***@host:5432/db
//	host=localhost user=abys password=secret dbname=test -> host=localhost user=*** password=*** dbname=test
func RedactDSN(dsn string) string {
	// URL format: postgres://[user[:password]@]host[:port]/dbname
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			// Fallback to regex if parsing fails (e.g. password with / confuses parser)
			re := regexp.MustCompile(`^(postgres(?:ql)?://)[^@]*@`)
			return re.ReplaceAllString(dsn, "${1}***@")
		}
		// If parsing succeeded but user info is empty (edge case), fall back to regex
		if parsed.User != nil && parsed.User.Username() == "" {
			re := regexp.MustCompile(`^(postgres(?:ql)?://)[^@]*@`)
			return re.ReplaceAllString(dsn, "${1}***@")
		}
		// Rebuild URL manually to avoid URL encoding of ***
		scheme := parsed.Scheme + "://"
		host := parsed.Host
		path := parsed.Path
		rawQuery := parsed.RawQuery
		if parsed.User != nil {
			// Redact user info entirely
			return scheme + "***@" + host + path + rawQuerySuffix(rawQuery)
		}
		return scheme + host + path + rawQuerySuffix(rawQuery)
	}
	// Key=value format: key1=val1 key2=val2 ...
	// Match password, user, username followed by = and value (until space or end)
	// For values with spaces, we match greedily until the next key= or end
	re := regexp.MustCompile(`\b(password|user|username)=([^\s]+)`)
	return re.ReplaceAllString(dsn, "${1}=***")
}

func rawQuerySuffix(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	return "?" + rawQuery
}

// EnsureTestDSN validates that the DSN points to a test database (name ends with
// _test) and returns the redacted DSN for logging. If the DSN is not a test
// database, it panics with a message that does NOT contain the full DSN.
func EnsureTestDSN(dsn string) string {
	redacted := RedactDSN(dsn)
	// Check for _test suffix in database name (URL or key=value format)
	isTestDB := false
	if strings.Contains(dsn, "_test") {
		// More precise: check that the database name component ends with _test
		// URL format: .../dbname?params
		if idx := strings.LastIndex(dsn, "/"); idx >= 0 {
			dbPart := dsn[idx+1:]
			if strings.HasPrefix(dbPart, "_test") || strings.Contains(dbPart, "_test") {
				// Check it's the db name, not a param
				if q := strings.IndexByte(dbPart, '?'); q >= 0 {
					dbPart = dbPart[:q]
				}
				if strings.HasSuffix(dbPart, "_test") {
					isTestDB = true
				}
			}
		}
		// key=value format: dbname=xxx_test
		if strings.Contains(dsn, "dbname=") {
			for _, part := range strings.Fields(dsn) {
				if strings.HasPrefix(part, "dbname=") && strings.HasSuffix(part, "_test") {
					isTestDB = true
					break
				}
			}
		}
	}
	if !isTestDB {
		panic("testsupport: DSN no apunta a una base de datos de test (sufijo _test); detectado: " + redacted)
	}
	return redacted
}
