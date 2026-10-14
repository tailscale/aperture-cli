package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// longFlags are the flag names the deprecated single-dash form may spell.
// gnuForm rewrites -name to --name for these and nothing else.
var longFlags = []string{
	"endpoint", "bridge", "provider", "model", "port",
	"config", "debug", "version", "help",
}

// valueFlags are the flags that take a value, in every spelling. A value can
// be spelled like a command name, so the scanner must skip it rather than
// mistake it for one.
var valueFlags = []string{
	"endpoint", "e", "bridge", "b", "provider", "p", "model", "m", "config", "c",
}

// tailStart returns the index where a client command's tail begins: the
// first token naming a client, or -- , or the end. A flag's value is never
// taken for a command.
//
// ponytail: a bridge named after a client binary makes tailStart stop early,
// so a deprecated single-dash flag after it errors instead of parsing. The
// fix is a real parser and this corner is not worth one.
func tailStart(args []string, clientNames []string) int {
	expectingValue := false
	for i, arg := range args {
		if arg == "--" {
			return i
		}
		if expectingValue {
			expectingValue = false
			continue
		}
		if slices.Contains(clientNames, arg) {
			return i
		}
		if arg == "" || arg == "-" || !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if !hasValue && slices.Contains(valueFlags, name) {
			expectingValue = true
		}
	}
	return len(args)
}

// gnuForm rewrites the deprecated single-dash long flags (-endpoint) to the
// GNU form (--endpoint) so pflag accepts them, everywhere before a client
// command's tail. Rewriting stops at a client's name and at --: inside a
// client's tail a single-dash long flag is the client's own, and the
// deprecated forms only ever applied to aperture's own flags. The rewritten
// names come back so the caller can warn.
//
// Deprecated: the single-dash long form exists for backward compatibility
// (ADR 0007) and will be removed in a future release.
func gnuForm(args []string, long []string, clientNames []string) (rewritten, deprecated []string) {
	head, tail := args[:tailStart(args, clientNames)], args[tailStart(args, clientNames):]
	for _, arg := range head {
		if rest, ok := strings.CutPrefix(arg, "-"); ok && !strings.HasPrefix(rest, "-") && len(rest) > 1 {
			name, value, hasValue := strings.Cut(rest, "=")
			if slices.Contains(long, name) {
				deprecated = append(deprecated, name)
				arg = "--" + name
				if hasValue {
					arg += "=" + value
				}
			}
		}
		rewritten = append(rewritten, arg)
	}
	return append(rewritten, tail...), deprecated
}

// versionRequested reports whether --version or -v, with no value or a true
// one, appears before a client command's tail. After a client's name the
// flag belongs to the client, so `aperture --version pi` prints the version
// while `aperture pi --version` passes it through. A later flag overrides
// an earlier one, same as splitLaunchArgs' --debug.
func versionRequested(args []string, clientNames []string) bool {
	requested := false
	for _, arg := range args[:tailStart(args, clientNames)] {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if name != "version" && name != "v" {
			continue
		}
		requested = true
		if hasValue {
			if on, err := strconv.ParseBool(value); err == nil {
				requested = on
			}
		}
	}
	return requested
}

// warnDeprecated writes the deprecation warning for each single-dash long
// flag gnuForm rewrote.
func warnDeprecated(w io.Writer, deprecated []string) {
	for _, name := range deprecated {
		fmt.Fprintf(w, "warning: -%s is deprecated and will be removed in a future release; use --%s instead\n", name, name)
	}
}
