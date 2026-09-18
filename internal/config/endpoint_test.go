package config

import "testing"

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		bridgeID string
		want     Endpoint
		wantErr  bool
	}{
		{name: "bare host assumes http", in: "ai", want: Endpoint{URL: "http://ai"}},
		{name: "trailing slash trimmed", in: "http://ai/", want: Endpoint{URL: "http://ai"}},
		{name: "surrounding space trimmed", in: "  http://ai  ", want: Endpoint{URL: "http://ai"}},
		{name: "https preserved", in: "https://aperture.example.ts.net", want: Endpoint{URL: "https://aperture.example.ts.net"}},
		{
			name:     "bridge recorded",
			in:       "aperture",
			bridgeID: "bridge-abcdef",
			want:     Endpoint{URL: "http://aperture", BridgeID: "bridge-abcdef"},
		},
		{name: "empty", in: "   ", wantErr: true},
		{name: "scheme only", in: "http://", wantErr: true},
		{name: "unsupported scheme", in: "ftp://ai", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEndpoint(tt.in, tt.bridgeID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseEndpoint(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEndpoint(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseEndpoint(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}
