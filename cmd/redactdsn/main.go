// redactdsn emits a PostgreSQL DSN with its credentials (userinfo) redacted.
//
// It exists so the Makefile stops redacting DSNs with a `sed` regular
// expression: `s#(://)[^/@]*@#…#` is bypassed by a password containing `/`, which
// is a legal password character and would print the secret in the integration
// log. This command parses the DSN as a URL and reuses the SAME redaction
// helper the test suites use (testsupport.RedactDSN, ADR D30), so a single
// audited implementation decides what "redacted" means.
//
// Usage:
//
//	redactdsn "$DATABASE_URL"     # argv
//	printf %s "$DSN" | redactdsn   # stdin (no trailing newline)
//
// The result is written to stdout with no trailing newline: the caller only
// wants to embed it in an `echo` of a make recipe, and a stray newline is
// harmless there but would be noise in a `$(...)` substitution.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/miky/abys-invest/internal/testsupport"
)

func main() {
	input, err := readInput(os.Args[1:], os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "redactdsn:", err)
		os.Exit(2)
	}
	fmt.Print(testsupport.RedactDSN(input))
}

// readInput returns the DSN to redact: the STDIN content when it is not empty,
// otherwise the argv arguments joined by spaces (key=value DSNs are accepted
// either way).
//
// STDIN wins over argv so `printf %s "$DSN" | redactdsn` never leaks an
// accidentally-passed credential through argv, where it would be visible in the
// process table (`ps`), and argv remains available for interactive use.
func readInput(args []string, stdin io.Reader) (string, error) {
	var sb strings.Builder
	// A DSN is a single short line; 64 KiB is far beyond any real one and keeps
	// a runaway pipe from growing without bound.
	if _, err := io.Copy(&sb, io.LimitReader(stdin, 64*1024)); err != nil {
		return "", fmt.Errorf("leyendo STDIN: %w", err)
	}
	if s := strings.TrimSpace(sb.String()); s != "" {
		return s, nil
	}
	if len(args) == 0 {
		return "", fmt.Errorf("sin DSN: pasalo por argv o por STDIN")
	}
	return strings.Join(args, " "), nil
}
