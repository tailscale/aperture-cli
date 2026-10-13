package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// longFlags are the flag names the deprecated single-dash form may spell.
// gnuForm rewrites -name to --name for these and nothing else.
var longFlags = []string{
	"endpoint", "bridge", "provider", "model", "port",
	"config", "debug", "version", "help",
}

// gnuForm rewrites the deprecated single-dash long flags (-endpoint) to the
// GNU form (--endpoint) so pflag accepts them. Single letters are shorthands
// and stay as they are. Everything after -- is verbatim: it belongs to the
// command or the launched client. The rewritten names come back so the
// caller can warn.
//
// Deprecated: the single-dash long form exists for backward compatibility
// (ADR 0007) and will be removed in a future release.
func gnuForm(args []string, long []string) (rewritten, deprecated []string) {
	for i, arg := range args {
		if arg == "--" {
			return append(rewritten, args[i:]...), deprecated
		}
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
	return rewritten, deprecated
}

// warnDeprecated writes the deprecation warning for each single-dash long
// flag gnuForm rewrote.
func warnDeprecated(w io.Writer, deprecated []string) {
	for _, name := range deprecated {
		fmt.Fprintf(w, "warning: -%s is deprecated and will be removed in a future release; use --%s instead\n", name, name)
	}
}
