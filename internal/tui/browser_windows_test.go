package tui

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsOpenURLPreservesURLData(t *testing.T) {
	const link = "https://login.example/a/token?next=x&calc.exe&value=%PATH%|test^value"
	previous := shellExecute
	t.Cleanup(func() { shellExecute = previous })
	wantErr := errors.New("no URL association")
	called := false
	shellExecute = func(hwnd windows.Handle, verb, file, args, cwd *uint16, show int32) error {
		called = true
		if hwnd != 0 || windows.UTF16PtrToString(verb) != "open" || windows.UTF16PtrToString(file) != link || args != nil || cwd != nil || show != windows.SW_SHOWNORMAL {
			t.Fatal("URL was not passed intact as the native opener's sole target")
		}
		return wantErr
	}
	if err := platformOpenURL(link); err != wantErr || !called {
		t.Fatalf("open = %v, called = %t; want native error", err, called)
	}
	called = false
	if err := platformOpenURL("https://login.example/a/\x00token"); err == nil || called {
		t.Fatalf("NUL URL = %v, called = %t; want rejection before native call", err, called)
	}
}
