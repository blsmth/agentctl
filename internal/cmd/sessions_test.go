package cmd

import (
	"testing"
	"time"
)

func TestParseDateFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantY   int
		wantM   time.Month
		wantD   int
	}{
		{
			name:  "valid US date",
			input: "03-30-2026",
			wantY: 2026, wantM: time.March, wantD: 30,
		},
		{
			name:    "invalid ISO date",
			input:   "2026-03-30",
			wantErr: true,
		},
		{
			name:    "garbage input",
			input:   "not-a-date",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDateFlag(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseDateFlag(%q) expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDateFlag(%q) unexpected error: %v", tc.input, err)
			}
			y, m, d := got.Date()
			if y != tc.wantY || m != tc.wantM || d != tc.wantD {
				t.Errorf("ParseDateFlag(%q) = %v, want %d-%d-%d", tc.input, got, tc.wantY, tc.wantM, tc.wantD)
			}
		})
	}
}
