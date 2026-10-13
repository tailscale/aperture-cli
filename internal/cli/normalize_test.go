package cli

import (
	"strings"
	"testing"
)

func TestGnuForm(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		want       []string
		deprecated []string
	}{
		{
			name:       "single-dash long flag is rewritten",
			in:         []string{"-endpoint", "http://ai"},
			want:       []string{"--endpoint", "http://ai"},
			deprecated: []string{"endpoint"},
		},
		{
			name:       "attached value is kept",
			in:         []string{"-endpoint=http://ai"},
			want:       []string{"--endpoint=http://ai"},
			deprecated: []string{"endpoint"},
		},
		{
			name: "GNU form passes through",
			in:   []string{"--endpoint", "http://ai"},
			want: []string{"--endpoint", "http://ai"},
		},
		{
			name: "shorthand is not a long flag",
			in:   []string{"-e", "http://ai"},
			want: []string{"-e", "http://ai"},
		},
		{
			name: "unknown single-dash flag passes through",
			in:   []string{"-search"},
			want: []string{"-search"},
		},
		{
			name:       "everything after the separator is verbatim",
			in:         []string{"-endpoint", "http://ai", "--", "-endpoint", "-model"},
			want:       []string{"--endpoint", "http://ai", "--", "-endpoint", "-model"},
			deprecated: []string{"endpoint"},
		},
		{
			name: "a lone dash is not a flag",
			in:   []string{"-"},
			want: []string{"-"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, deprecated := gnuForm(tt.in, longFlags)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("gnuForm(%v) = %v, want %v", tt.in, got, tt.want)
			}
			if strings.Join(deprecated, " ") != strings.Join(tt.deprecated, " ") {
				t.Errorf("gnuForm(%v) deprecated = %v, want %v", tt.in, deprecated, tt.deprecated)
			}
		})
	}
}

func TestWarnDeprecated(t *testing.T) {
	var out strings.Builder
	warnDeprecated(&out, []string{"endpoint"})
	got := out.String()
	if !strings.Contains(got, "-endpoint") || !strings.Contains(got, "--endpoint") || !strings.Contains(got, "deprecated") {
		t.Errorf("warning = %q, want the old and new forms and the word deprecated", got)
	}
}
