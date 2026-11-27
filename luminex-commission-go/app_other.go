//go:build !(darwin && cgo)

package main

// Everything but the Mac app build (see app_darwin.go): the tool runs in a
// terminal or console window, which is its log and its way to quit.

func inAppBundle() bool                     { return false }
func logToFile() (string, error)            { return "", nil }
func runMenuBar(title, url, logPath string) {}
