package testsupport

import (
	"testing"
)

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "URL with user:pass",
			input:    "postgres://user:pass@host:5432/db",
			expected: "postgres://***@host:5432/db",
		},
		{
			name:     "URL with password containing slash",
			input:    "postgres://user:pass/word@host:5432/db",
			expected: "postgres://***@host:5432/db",
		},
		{
			name:     "URL with password containing special chars",
			input:    "postgres://user:p%40ss@host:5432/db",
			expected: "postgres://***@host:5432/db",
		},
		{
			name:     "URL with user only",
			input:    "postgres://user@host:5432/db",
			expected: "postgres://***@host:5432/db",
		},
		{
			name:     "URL no auth",
			input:    "postgres://host:5432/db",
			expected: "postgres://host:5432/db",
		},
		{
			name:     "postgresql:// scheme",
			input:    "postgresql://user:pass@host:5432/db",
			expected: "postgresql://***@host:5432/db",
		},
		{
			name:     "key=value format with password",
			input:    "host=localhost user=abys password=secret dbname=test",
			expected: "host=localhost user=*** password=*** dbname=test",
		},
		{
			name:     "key=value format with username",
			input:    "host=localhost user=abys dbname=test",
			expected: "host=localhost user=*** dbname=test",
		},
		{
			name:     "key=value format with password containing space (redacts first word only)",
			input:    "host=localhost password=sec ret dbname=test",
			expected: "host=localhost password=*** ret dbname=test",
		},
		{
			name:     "key=value format with password containing equals",
			input:    "host=localhost password=abc=def dbname=test",
			expected: "host=localhost password=*** dbname=test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactDSN(tt.input)
			if result != tt.expected {
				t.Errorf("RedactDSN(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEnsureTestDSN(t *testing.T) {
	// Valid test DB URL
	redacted := EnsureTestDSN("postgres://user:pass@localhost:5432/abys_test?sslmode=disable")
	if redacted != "postgres://***@localhost:5432/abys_test?sslmode=disable" {
		t.Errorf("EnsureTestDSN valid: got %q", redacted)
	}

	// Valid test DB key=value
	redacted = EnsureTestDSN("host=localhost user=abys password=secret dbname=abys_test sslmode=disable")
	if redacted != "host=localhost user=*** password=*** dbname=abys_test sslmode=disable" {
		t.Errorf("EnsureTestDSN key=value: got %q", redacted)
	}

	// Non-test DB should panic
	defer func() {
		if r := recover(); r == nil {
			t.Error("EnsureTestDSN should panic for non-test DB")
		}
	}()
	EnsureTestDSN("postgres://user:pass@localhost:5432/abys?sslmode=disable")
}
