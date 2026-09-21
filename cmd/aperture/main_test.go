package main

import (
	"flag"
	"testing"
)

// -bridge= has to beat APERTURE_BRIDGE: the documented precedence is flag over
// environment, and an empty flag is the only way a shell with the variable set
// can ask for the saved endpoint.
func TestFlagOrEnv(t *testing.T) {
	t.Setenv("APERTURE_BRIDGE", "work")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"absent flag reads the environment", nil, "work"},
		{"flag wins", []string{"-bridge=home"}, "home"},
		{"explicitly empty flag wins", []string{"-bridge="}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("aperture", flag.ContinueOnError)
			fs.String("bridge", "", "")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := flagOrEnv(fs, "bridge", "APERTURE_BRIDGE"); got != tc.want {
				t.Errorf("flagOrEnv = %q, want %q", got, tc.want)
			}
		})
	}
}
