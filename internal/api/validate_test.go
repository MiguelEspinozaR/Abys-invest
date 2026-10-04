// Package api tests for validation helpers.
package api

import (
	"strings"
	"testing"
)

func TestParseFloatQuery(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		def         float64
		wantVal     float64
		wantErr     bool
		errContains string
	}{
		{"empty uses default", "", 0.02, 0.02, false, ""},
		{"valid positive", "0.05", 0.02, 0.05, false, ""},
		{"valid zero", "0", 0.02, 0.0, false, ""},
		{"valid integer", "2", 0.02, 2.0, false, ""},
		{"negative rejected", "-1", 0.02, 0, true, "parámetro numérico inválido"},
		{"NaN rejected", "NaN", 0.02, 0, true, "parámetro numérico inválido"},
		{"Inf rejected", "Infinity", 0.02, 0, true, "parámetro numérico inválido"},
		{"-Inf rejected", "-Infinity", 0.02, 0, true, "parámetro numérico inválido"},
		{"invalid text rejected", "abc", 0.02, 0, true, "parámetro numérico inválido"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFloatQuery(tc.input, tc.def)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tc.input)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for input %q: %v", tc.input, err)
				}
				if got != tc.wantVal {
					t.Fatalf("for input %q: want %v, got %v", tc.input, tc.wantVal, got)
				}
			}
		})
	}
}
