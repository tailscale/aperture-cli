package tui

import "golang.org/x/sys/windows"

var shellExecute = windows.ShellExecute

func platformOpenURL(url string) error {
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	// The URL is a file argument to the native URL handler, never input to
	// cmd.exe. Query separators and shell punctuation remain URL data.
	return shellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
