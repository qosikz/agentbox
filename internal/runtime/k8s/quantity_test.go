package k8s

import "testing"

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{name: "plain integer", in: "2", want: 2},
		{name: "decimal", in: "1.5", want: 1.5},
		{name: "milli", in: "500m", want: 0.5},
		{name: "milli integer", in: "1500m", want: 1.5},
		{name: "binary Ki", in: "1Ki", want: 1024},
		{name: "binary Mi", in: "512Mi", want: 512 * 1024 * 1024},
		{name: "binary Gi", in: "2Gi", want: 2 * 1024 * 1024 * 1024},
		{name: "decimal k", in: "1k", want: 1000},
		{name: "decimal M", in: "5M", want: 5_000_000},
		{name: "decimal G", in: "1G", want: 1_000_000_000},
		{name: "exponent", in: "1e3", want: 1000},

		{name: "empty", in: "", wantErr: true},
		{name: "whitespace", in: " 1Gi", wantErr: true},
		{name: "unknown suffix", in: "1X", wantErr: true},
		{name: "uppercase K is invalid in kubernetes", in: "1K", wantErr: true},
		{name: "suffix only", in: "Mi", wantErr: true},
		{name: "double suffix", in: "1MiMi", wantErr: true},
		{name: "negative", in: "-1", wantErr: true},
		{name: "zero", in: "0", wantErr: true},
		{name: "not a number", in: "lots", wantErr: true},
		{name: "injection attempt", in: "1Gi\nprivileged: true", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseQuantity(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseQuantity(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQuantity(%q) returned unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseQuantity(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
