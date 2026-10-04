package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miky/abys-invest/internal/testsupport"
)

// readInput + RedactDSN is the whole contract of the command, so the table is
// asserted against the same composition main() performs. The subprocess test at
// the end covers the wiring (STDIN/argv → stdout), which is the only part a
// table on the function cannot prove.
func TestRedact(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		stdin    string
		want     string
		wantFail bool
	}{
		{
			name:  "password con slash (evadía el sed anterior)",
			stdin: "postgres://abys:pa/ss@localhost:55432/abys_test",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "password con arroba",
			stdin: "postgres://abys:pa@ss@localhost:55432/abys_test",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "password URL-encoded con arroba",
			stdin: "postgres://abys:p%40ss%2Fword@localhost:55432/abys_test",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "sin userinfo",
			stdin: "postgres://localhost:55432/abys_test",
			want:  "postgres://localhost:55432/abys_test",
		},
		{
			name:  "solo usuario",
			stdin: "postgres://abys@localhost:55432/abys_test",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "host IPv6",
			stdin: "postgres://abys:secret@[2001:db8::1]:5432/abys_test",
			want:  "postgres://***@[2001:db8::1]:5432/abys_test",
		},
		{
			name:  "placeholder local (key=value)",
			stdin: "host=localhost port=55432 user=abys password=abys dbname=abys_test sslmode=disable",
			want:  "host=localhost port=55432 user=*** password=*** dbname=abys_test sslmode=disable",
		},
		{
			name: "argv en vez de STDIN",
			args: []string{"postgres://abys:pa/ss@localhost:55432/abys_test"},
			want: "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "STDIN gana sobre argv (no se imprime argv)",
			args:  []string{"postgres://ignored:ignored@localhost:55432/x"},
			stdin: "postgres://abys:pa/ss@localhost:55432/abys_test",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:  "nueva línea final no ensucia la salida",
			stdin: "postgres://abys:pa/ss@localhost:55432/abys_test\n",
			want:  "postgres://***@localhost:55432/abys_test",
		},
		{
			name:     "sin entrada",
			wantFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := readInput(tt.args, strings.NewReader(tt.stdin))
			if tt.wantFail {
				if err == nil {
					t.Fatalf("readInput() sin error con entrada vacía; DSN leída: %q", in)
				}
				return
			}
			if err != nil {
				t.Fatalf("readInput: %v", err)
			}
			if got := testsupport.RedactDSN(in); got != tt.want {
				t.Errorf("redact(%q) = %q, want %q", in, got, tt.want)
			}
		})
	}
}

// TestRedactNoCredentialsInOutput is the CA of (b): for every DSN whose
// password contains a `/` (the character that bypassed the old `sed`), the
// output must not contain the secret at all.
func TestRedactNoCredentialsInOutput(t *testing.T) {
	const secret = "sup3r/s3cr3t"
	dsn := "postgres://abys:" + secret + "@localhost:55432/abys_test"
	in, err := readInput(nil, strings.NewReader(dsn))
	if err != nil {
		t.Fatalf("readInput: %v", err)
	}
	out := testsupport.RedactDSN(in)
	if strings.Contains(out, secret) {
		t.Fatalf("la salida filtra la contraseña: %q", out)
	}
	if strings.Contains(out, "abys:") {
		t.Fatalf("la salida filtra el userinfo: %q", out)
	}
}

// TestBinaryRedacts covers the wiring of the real binary: build once, then feed
// it by STDIN and by argv. `go run` is NOT used here because it would re-enter
// the module from inside a test process.
func TestBinaryRedacts(t *testing.T) {
	if testing.Short() {
		t.Skip("build del binario omitido en -short")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("ruta del módulo: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "redactdsn")
	build := exec.Command("go", "build", "-o", bin, "./cmd/redactdsn")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	const dsn = "postgres://abys:pa/ss@localhost:55432/abys_test"
	const want = "postgres://***@localhost:55432/abys_test"

	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
	}{
		{"stdin", exec.Command(bin)},
		{"argv", exec.Command(bin, dsn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "stdin" {
				tc.cmd.Stdin = bytes.NewBufferString(dsn + "\n")
			}
			out, err := tc.cmd.Output()
			if err != nil {
				t.Fatalf("redactdsn: %v", err)
			}
			if string(out) != want {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		})
	}
}
