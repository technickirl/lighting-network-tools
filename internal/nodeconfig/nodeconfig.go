// Package nodeconfig is the LumiNode / LumiCore editor: process blocks, IOs,
// Quick Setup, startup buffers, profiles, device name and IP settings, with live
// DMX port state. A tool mounts its API and serves its page.
package nodeconfig

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/technickirl/lighting-network-tools/internal/luminex"

	"golang.org/x/net/websocket"
)

//go:embed public
var publicFS embed.FS

// Options connects the node API to the tool around it.
type Options struct {
	// Locked says whether writes to the device at ip must wait, and why. Nil means
	// never. While it is true, writes get 409 and the live poller leaves it alone.
	Locked func(ip string) (bool, string)
	// Serial is the serial the tool expects at ip, or "" when it does not know. Nil means never. A write to an
	// address where the node answers with another serial gets 409.
	Serial func(ip string) string
	// Version is the app's release. A page compares it on reconnecting and
	// reloads when it has changed, so it never runs on an older build's code.
	Version string
}

var options Options

func locked(ip string) (bool, string) {
	if options.Locked == nil {
		return false, ""
	}
	return options.Locked(ip)
}

// wrongNode says why a write to ip must not go: the node answering there is not the one the tool lists at that
// address. It refuses only on proof. No answer, a password prompt or a reply without a serial are left to the
// write itself to report.
func wrongNode(ip string) string {
	if options.Serial == nil {
		return ""
	}
	want := options.Serial(ip)
	if want == "" {
		return ""
	}
	info, err := lumiObject(ip, "/api/deviceinfo")
	if err != nil {
		return ""
	}
	got := strings.TrimSpace(str(info, "serial", ""))
	if got == "" || strings.EqualFold(got, want) {
		return ""
	}
	luminex.Warnf("[%s] %s answered as serial %s: write refused", want, ip, got)
	return fmt.Sprintf("Another node (serial %s, not %s) answers at %s. Nothing was sent. Scan again.", got, want, ip)
}

// Mount registers the node API and the live WebSocket on mux.
func Mount(mux *http.ServeMux, opts Options) {
	options = opts
	registerRoutes(mux)
	mux.HandleFunc("GET /api/app-version", func(w http.ResponseWriter, r *http.Request) {
		ok(w, obj{"version": options.Version})
	})
	mux.Handle("GET /api/live", websocket.Server{Handler: wsHandler}) // Origin is checked by luminex.LocalOnly and the other-devices guard
}

// Assets is the page: index.html, nodeconfig.js and nodeconfig.css.
func Assets() fs.FS {
	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		panic(err) // the path is fixed at build time
	}
	return sub
}
