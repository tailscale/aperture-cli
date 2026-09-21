package tui

import (
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This runs in make check on Linux too: Windows URL opening must not depend
// on process argument quoting. Native argument/error tests run on Windows.
func TestWindowsLoginOpenerDoesNotLaunchCommands(t *testing.T) {
	windowsBuild := build.Default
	windowsBuild.GOOS = "windows"
	files, err := filepath.Glob("browser*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		match, err := windowsBuild.MatchFile(".", name)
		if err != nil {
			t.Fatal(err)
		}
		if !match {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if path == "os/exec" {
				t.Errorf("Windows login opener %s imports os/exec; use a native URL API instead of commands", name)
			}
		}
	}
}
