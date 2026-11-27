package main

// Luminex Commission: one tool for the whole job. Scan, address and name devices,
// factory reset them, and open any node's configure page from the same list.
//
//   ./LuminexCommission                 normal
//   DRY_RUN=1 ./LuminexCommission       log write requests instead of sending them
//   --no-open                           don't launch the browser
//   --port N                            listen on a specific port (default 3800)
//   --no-lan                            start with access from other devices off
//                                       (it is on at start by default; --lan is
//                                       still accepted and changes nothing)
//   --save-pin 123456                   save the PIN for other devices and exit
//                                       ("" goes back to a random PIN each time)
//
// The code is in internal/commission and internal/nodeconfig.
// This is the program around them.

import (
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/commission"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"github.com/technickirl/lighting-network-tools/internal/nodeconfig"
	"github.com/technickirl/lighting-network-tools/internal/sacn"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
)

// version is the release this build belongs to. Builds from a v* tag set it
// with -ldflags "-X main.version=<tag>"; a plain `go run .` shows this default.
var version = "2.1.0"

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		luminex.Warnf("could not open browser: %v", err)
	}
}

const configurePath = "/configure/"

// newMux puts the device list at / and the node configure page's files under
// /configure/: the device list's page loads them into its Node view. /configure/
// itself was a page of its own before 2.0.0, so an old bookmark or open tab is
// sent to the same node in the Node view.
// The sACN view is a tab of the device list's page; its API is /api/sacn.
func newMux(opts commission.Options) *http.ServeMux {
	mux := http.NewServeMux()
	opts.Version, opts.Title, opts.ConfigureURL = version, "Luminex Commission", configurePath
	commission.Mount(mux, opts)
	nodeconfig.Mount(mux, nodeconfig.Options{Locked: commission.Locked, Version: version})
	sacn.Mount(mux)
	page := http.StripPrefix(configurePath, http.FileServer(http.FS(nodeconfig.Assets())))
	mux.HandleFunc(configurePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == configurePath {
			to := "/"
			if ip := r.URL.Query().Get("ip"); ip != "" {
				to += "#" + url.Values{"node": {ip}, "id": {r.URL.Query().Get("id")}}.Encode()
			}
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		page.ServeHTTP(w, r)
	})
	return mux
}

// savePIN is --save-pin: it saves the PIN other devices sign in with (or
// forgets it, for "") without starting the tool, so a computer with no screen
// can be given one over SSH. Returns the exit code.
func savePIN(pin string) int {
	lan := luminex.NewLANAccess(0, nil)
	lan.SetPINFile(luminex.DefaultPINFile("luminex-commission"))
	if err := lan.SavePIN(pin); err != nil {
		luminex.Errorf("could not save the PIN: %v", err)
		return 1
	}
	if pin == "" {
		fmt.Println("Saved PIN removed: access from other devices gets a new random PIN each time.")
	} else {
		fmt.Println("PIN saved. Access from other devices will use it every time it is turned on.")
	}
	return 0
}

// lanAtStart says whether access from other devices turns on at start: yes
// unless --no-lan (Nick, 13 Nov 2026). The last of --lan / --no-lan wins.
func lanAtStart(args []string) bool {
	on := true
	for _, a := range args {
		switch a {
		case "--lan":
			on = true
		case "--no-lan":
			on = false
		}
	}
	return on
}

// startLAN turns access from other devices on at start. With no saved PIN the
// random one is printed here too, since on a computer with no screen (a Pi)
// this log is the only place that shows it.
func startLAN(lan *luminex.LANAccess, out io.Writer) {
	st := lan.Enable()
	if st.SavedPIN != "" {
		fmt.Fprintln(out, "  Other devices: ON at start, with the saved PIN.")
	} else {
		fmt.Fprintf(out, "  Other devices: ON at start, PIN %s (random; set one with --save-pin).\n", st.PIN)
	}
	for _, u := range st.URLs {
		fmt.Fprintf(out, "  Open %s\n", u)
	}
	if len(st.URLs) == 0 {
		fmt.Fprintln(out, "  No network address yet; it starts listening as soon as one appears.")
	}
	fmt.Fprintln(out)
}

func main() {
	noOpen := false
	port := 3800
	if p := os.Getenv("PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-open":
			noOpen = true
		case "--lan", "--no-lan":
			// read by lanAtStart
		case "--save-pin":
			if i+1 >= len(args) {
				luminex.Errorf("--save-pin needs six digits, or \"\" for a random PIN each time")
				os.Exit(2)
			}
			os.Exit(savePIN(args[i+1]))
		case "--port":
			if i+1 < len(args) {
				port, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}
	if port <= 0 {
		port = 3800
	}

	// Started as the Mac app there is no terminal: the log goes to a file and a
	// menu bar icon stands in for the window (app_darwin.go).
	app, logPath := inAppBundle(), ""
	if app {
		var err error
		if logPath, err = logToFile(); err != nil {
			luminex.Warnf("no log file: %v", err)
		}
	}

	mux := newMux(commission.Options{})

	var ln net.Listener
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		luminex.Warnf("port %d busy, trying %d", port, port+1)
		port++
	}
	if err != nil {
		luminex.Errorf("cannot start server: %v", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	mode := "LIVE — changes are real"
	if luminex.DryRun {
		mode = "DRY RUN — nothing will be changed"
	}
	fmt.Println()
	fmt.Println("  ┌──────────────────────────────────────────┐")
	fmt.Printf("  │  Luminex Commission v%-20s│\n", version)
	fmt.Printf("  │  %-40s│\n", url)
	fmt.Printf("  │  %-40s│\n", mode)
	fmt.Println("  └──────────────────────────────────────────┘")
	if app {
		fmt.Println("  Quit from the menu bar icon.")
	} else {
		fmt.Println("  Keep this window open. Close it to quit.")
	}
	fmt.Println()
	if !noOpen {
		openBrowser(url)
	}
	// Access from other devices (an iPad): on at start unless --no-lan; the page
	// on this computer can turn it off.
	lan := luminex.NewLANAccess(port, mux)
	lan.SetPINFile(luminex.DefaultPINFile("luminex-commission"))
	mux.HandleFunc("/api/lan", lan.ServeLANSetting)
	if lanAtStart(args) {
		startLAN(lan, os.Stdout)
	}
	commission.Start()
	// Ctrl-C, or closing the Terminal window, would otherwise just cut the sACN output off: receivers then hold
	// the last levels for their 2.5 s timeout. Stop sends each universe's "stream terminated" packets first.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-quit
		sacn.Output().Stop()
		os.Exit(0)
	}()
	serve := func() {
		if err := http.Serve(ln, luminex.LocalOnly(mux)); err != nil {
			luminex.Errorf("server: %v", err)
			os.Exit(1)
		}
	}
	if app {
		go serve()
		title := "Luminex Commission " + version
		if luminex.DryRun {
			title += " (DRY RUN)"
		}
		runMenuBar(title, url, logPath) // holds the main thread until Quit
		return
	}
	serve()
}
