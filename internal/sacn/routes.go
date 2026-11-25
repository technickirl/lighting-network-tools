package sacn

// The viewer's API. The receiver starts the first time the page asks for it, so
// a tool nobody opens the sACN view in never binds port 5568 or joins a group.
// The typed list of universes is remembered between runs in the user's config
// folder (luminex-commission/sacn.json; LUMINEX_SACN_FILE moves it).

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

var (
	sharedMu  sync.Mutex
	shared    *Receiver
	sharedErr error
)

// settingsPath is a variable so tests can point it at a temporary file.
var settingsPath = func() string {
	if p := os.Getenv("LUMINEX_SACN_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "luminex-commission", "sacn.json")
}

type settings struct {
	Watch string `json:"watch"`
	// The output's last settings, offered again on the next run (it never starts by itself).
	Interface string `json:"interface,omitempty"`
	Priority  *int   `json:"priority,omitempty"`
	Name      string `json:"name,omitempty"`
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveSettings(s settings) error {
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Shared is the one receiver of this process, opened on first use. A failure to
// open is tried again on the next call.
func Shared() (*Receiver, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		return shared, nil
	}
	r, err := Open(Port)
	if err != nil {
		if sharedErr == nil || sharedErr.Error() != err.Error() {
			luminex.Warnf("sACN: %v", err)
		}
		sharedErr = err
		return nil, err
	}
	sharedErr = nil
	if us, err := ParseUniverses(loadSettings().Watch); err == nil {
		r.SetWatch(us)
	}
	// Listen on the universes this tool sends too, to see who else is on them.
	r.mu.Lock()
	r.also = out.sender.Universes
	r.mu.Unlock()
	luminex.Logf("sACN: listening on UDP %d", r.Port())
	shared = r
	return r, nil
}

// SetSharedForTests puts a receiver in place of the one Shared would open.
func SetSharedForTests(r *Receiver) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	shared, sharedErr = r, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Mount registers the sACN viewer's and controller's API on mux.
func Mount(mux *http.ServeMux) {
	mountOutput(mux)
	mux.HandleFunc("GET /api/sacn", func(w http.ResponseWriter, r *http.Request) {
		rx, err := Shared()
		if err != nil {
			writeJSON(w, 200, Status{Error: err.Error(), Port: Port, Max: MaxWatched, Watch: loadSettings().Watch,
				Interfaces: []string{}, Universes: []UniverseInfo{}, Discovered: []DiscoveredInfo{}})
			return
		}
		writeJSON(w, 200, rx.Status())
	})
	mux.HandleFunc("GET /api/sacn/universe/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 || n > MaxUniverse {
			writeJSON(w, 400, map[string]string{"error": "Universes run from 1 to " + strconv.Itoa(MaxUniverse) + "."})
			return
		}
		rx, err := Shared()
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, rx.Universe(uint16(n)))
	})
	mux.HandleFunc("POST /api/sacn/watch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Universes string `json:"universes"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err := json.Unmarshal(b, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "Could not read the request."})
			return
		}
		us, err := ParseUniverses(body.Universes)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		rx, err := Shared()
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		rx.SetWatch(us)
		s := loadSettings()
		s.Watch = FormatUniverses(us)
		if err := saveSettings(s); err != nil {
			luminex.Warnf("sACN: could not save the universe list: %v", err)
		}
		luminex.Logf("sACN: listening to universes %q", FormatUniverses(us))
		writeJSON(w, 200, rx.Status())
	})
}
