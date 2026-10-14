package cli

import (
	"reflect"
	"testing"
)

func TestResolvedTailFlag(t *testing.T) {
	t.Run("absent falls back to the environment", func(t *testing.T) {
		t.Setenv("APERTURE_ENDPOINT", "http://env")
		if got := resolvedTailFlag("", false, "APERTURE_ENDPOINT"); got != "http://env" {
			t.Errorf("resolvedTailFlag = %q, want the environment value", got)
		}
	})
	t.Run("an explicit empty flag overrides the environment", func(t *testing.T) {
		t.Setenv("APERTURE_BRIDGE", "work")
		if got := resolvedTailFlag("", true, "APERTURE_BRIDGE"); got != "" {
			t.Errorf("resolvedTailFlag = %q, want empty to win over the environment", got)
		}
	})
	t.Run("a flag value wins outright", func(t *testing.T) {
		t.Setenv("APERTURE_CONFIG", "env.json")
		if got := resolvedTailFlag("flag.json", true, "APERTURE_CONFIG"); got != "flag.json" {
			t.Errorf("resolvedTailFlag = %q, want the flag value", got)
		}
	})
}

func TestSplitLaunchArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		sel  launchSelection
		rest []string
		err  bool
	}{
		{
			name: "empty",
			in:   nil,
			sel:  launchSelection{},
		},
		{
			name: "aperture flags in every form",
			in:   []string{"--endpoint", "e1", "--bridge=work", "-p", "openai", "-m=gpt-5", "--debug"},
			sel: launchSelection{endpoint: "e1", bridge: "work", provider: "openai", model: "gpt-5", debug: true,
				endpointSet: true, bridgeSet: true},
		},
		{
			name: "shorthands",
			in:   []string{"-e", "e1", "-b", "work", "-c", "s.json", "-d", "--search"},
			sel: launchSelection{endpoint: "e1", bridge: "work", config: "s.json", debug: true,
				endpointSet: true, bridgeSet: true, configSet: true},
			rest: []string{"--search"},
		},
		{
			name: "double dash starts verbatim passthrough",
			in:   []string{"--endpoint", "e1", "--", "--endpoint", "other"},
			sel:  launchSelection{endpoint: "e1", endpointSet: true},
			rest: []string{"--endpoint", "other"},
		},
		{
			name: "an explicit empty value is still set, unlike an absent flag",
			in:   []string{"--bridge="},
			sel:  launchSelection{bridgeSet: true},
		},
		{
			name: "first unknown flag starts verbatim passthrough",
			in:   []string{"--model", "gpt-5", "--search", "--endpoint", "e1"},
			sel:  launchSelection{model: "gpt-5"},
			rest: []string{"--search", "--endpoint", "e1"},
		},
		{
			name: "the alias case: a client flag arrives first",
			in:   []string{"--search"},
			sel:  launchSelection{},
			rest: []string{"--search"},
		},
		{
			name: "a positional argument passes through",
			in:   []string{"resume"},
			sel:  launchSelection{},
			rest: []string{"resume"},
		},
		{
			name: "a flag without a value is an error",
			in:   []string{"--endpoint"},
			err:  true,
		},
		{
			name: "debug with a false value stays off",
			in:   []string{"--debug=false", "--search"},
			sel:  launchSelection{},
			rest: []string{"--search"},
		},
		{
			name: "debug with a junk value belongs to the client",
			in:   []string{"--debug=banana", "--search"},
			sel:  launchSelection{},
			rest: []string{"--debug=banana", "--search"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, rest, err := splitLaunchArgs(tt.in)
			if tt.err {
				if err == nil {
					t.Fatalf("splitLaunchArgs(%v) = %+v, want an error", tt.in, sel)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitLaunchArgs(%v): %v", tt.in, err)
			}
			if sel != tt.sel {
				t.Errorf("splitLaunchArgs(%v) selection = %+v, want %+v", tt.in, sel, tt.sel)
			}
			if !reflect.DeepEqual(rest, tt.rest) {
				t.Errorf("splitLaunchArgs(%v) passthrough = %v, want %v", tt.in, rest, tt.rest)
			}
		})
	}
}
