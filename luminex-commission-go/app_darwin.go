//go:build darwin && cgo

package main

// The Mac app: "Luminex Commission.app" (scripts/build-mac-app.sh) holds this
// binary built with cgo. Started from the bundle there is no Terminal window,
// so the log goes to a file and a menu bar icon gives Open, Show Log and Quit.
// The same binary started any other way (a shell, go run) behaves as the
// .command does: log on the terminal, no menu bar icon.

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void lcRunMenuBar(const char *title, const char *url, const char *logPath);
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// The menu bar needs the process's first thread, and main runs on the thread
// init ran on, so hold it from here.
func init() { runtime.LockOSThread() }

// inAppBundle says whether this binary was started from inside a .app.
func inAppBundle() bool {
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

// logToFile sends everything the tool prints to ~/Library/Logs/LuminexCommission.log
// and returns its path. The log of the run before is kept beside it as
// LuminexCommission.previous.log.
func logToFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "LuminexCommission.log")
	os.Rename(path, filepath.Join(dir, "LuminexCommission.previous.log"))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // it holds the PIN for other devices
	if err != nil {
		return "", err
	}
	// The descriptors too, so a crash report lands in the file.
	syscall.Dup2(int(f.Fd()), 1)
	syscall.Dup2(int(f.Fd()), 2)
	os.Stdout, os.Stderr = f, f
	fmt.Printf("Started %s (log lines carry the time only)\n", time.Now().Format("2 Jan 2006 15:04:05"))
	return path, nil
}

// runMenuBar shows the menu bar icon and runs the app's event loop. It must be
// called from main, and returns only by quitting the process.
func runMenuBar(title, url, logPath string) {
	C.lcRunMenuBar(C.CString(title), C.CString(url), C.CString(logPath))
}
