package luminex

// Access from other devices (an iPad on the show network). Commission turns it
// on at start (unless --no-lan); this package starts with it off.
// Turning it on opens a second listener on each of this computer's own IPv4
// addresses, on the same port as the localhost one, and makes a new six-digit
// PIN. A device on the network gets a PIN page first; the right PIN gives it a
// session cookie (HttpOnly, SameSite=Strict, so another site's page cannot use
// it). Only this computer's own page can see the PIN or turn access on or off.
// The localhost listener is untouched and stays behind LocalOnly.
//
// A PIN can be saved on this computer (SetPINFile): access then always turns on
// with that PIN instead of a new random one, so the iPad's PIN stays the same
// from show to show.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	lanCookie      = "luminex_session"
	lanMaxFails    = 5
	lanLockFor     = time.Minute
	lanSessionLife = 12 * time.Hour
)

// LANAccess manages access to the tool from other devices.
type LANAccess struct {
	port    int
	handler http.Handler
	read    func() []Iface // this computer's addresses; replaced by tests
	loopOK  bool           // tests listen on 127.0.0.1
	pinFile string         // where a saved PIN is kept; "" = none

	mu        sync.Mutex
	enabled   bool
	pin       string
	listeners map[string]*http.Server // "address:port" → server; closing it drops open connections too
	errs      map[string]string       // "address:port" → why it could not listen
	sessions  map[string]time.Time    // token → when it was made
	fails     int
	lockedTil time.Time
	stop      chan struct{}
}

// LANState is what this computer's page shows about access from other devices.
type LANState struct {
	Enabled bool     `json:"enabled"`
	PIN     string   `json:"pin,omitempty"`
	URLs    []string `json:"urls"`
	Errors  []string `json:"errors,omitempty"`
	Devices int      `json:"devices"` // sessions given out since it was turned on
	// SavedPIN is the PIN saved on this computer, used every time access is
	// turned on; "" means a new random PIN each time.
	SavedPIN string `json:"savedPin"`
	CanSave  bool   `json:"canSave"` // this tool keeps a PIN file
}

// NewLANAccess serves handler to other devices on port once Enable is called.
func NewLANAccess(port int, handler http.Handler) *LANAccess {
	return &LANAccess{port: port, handler: handler, read: LocalInterfaces}
}

// SetInterfacesForTests replaces the address list and allows loopback, so tests
// can listen on 127.0.0.1.
func (l *LANAccess) SetInterfacesForTests(read func() []Iface) { l.read, l.loopOK = read, true }

// SetPINFile keeps a saved PIN in path (created on first save, readable only
// by this user).
func (l *LANAccess) SetPINFile(path string) { l.pinFile = path }

// DefaultPINFile is where Commission keeps its saved PIN
// (LUMINEX_LAN_PIN_FILE moves it, for tests).
func DefaultPINFile(tool string) string {
	if p := os.Getenv("LUMINEX_LAN_PIN_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, tool, "lan-pin.json")
}

// ValidPIN is six digits.
func ValidPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// savedPIN reads the saved PIN; "" when there is none or the file is unusable
// (logged only when warn is set, so the page's poll does not fill the log).
func (l *LANAccess) savedPIN(warn bool) string {
	if l.pinFile == "" {
		return ""
	}
	b, err := os.ReadFile(l.pinFile)
	if err != nil {
		return ""
	}
	var f struct {
		PIN string `json:"pin"`
	}
	if json.Unmarshal(b, &f) != nil || !ValidPIN(f.PIN) {
		if warn {
			Warnf("other devices: ignoring the saved PIN in %s (not six digits)", l.pinFile)
		}
		return ""
	}
	return f.PIN
}

// SavePIN saves pin as the PIN to use every time access is turned on; "" goes
// back to a new random PIN each time. While access is on, a saved PIN also
// takes over straight away and signs every device out.
func (l *LANAccess) SavePIN(pin string) error {
	if l.pinFile == "" {
		return errors.New("this tool cannot save a PIN")
	}
	if pin != "" && !ValidPIN(pin) {
		return errors.New("the PIN must be six digits")
	}
	if pin == "" {
		if err := os.Remove(l.pinFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		Logf("other devices: saved PIN removed; a new random PIN each time")
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.pinFile), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"pin": pin})
	tmp := l.pinFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.pinFile); err != nil {
		return err
	}
	Logf("other devices: PIN saved on this computer")
	l.mu.Lock()
	if l.enabled && l.pin != pin {
		l.pin, l.sessions, l.fails, l.lockedTil = pin, map[string]time.Time{}, 0, time.Time{}
		Logf("other devices: now using the saved PIN; every device signed out")
	}
	l.mu.Unlock()
	return nil
}

// Enable opens the listeners with the saved PIN, or a new random one. Calling
// it while on makes a new random PIN (for now; the saved one is used next time)
// and signs every device out.
func (l *LANAccess) Enable() LANState {
	saved := l.savedPIN(true)
	l.mu.Lock()
	if saved != "" && !l.enabled {
		l.pin = saved
	} else {
		l.pin = newPIN()
	}
	l.sessions, l.fails, l.lockedTil = map[string]time.Time{}, 0, time.Time{}
	if !l.enabled {
		l.enabled, l.listeners, l.errs, l.stop = true, map[string]*http.Server{}, map[string]string{}, make(chan struct{})
		go WatchInterfaces(l.stop, l.read, 2*time.Second, func(_, _ []Iface) { l.reconcile() })
	}
	l.mu.Unlock()
	l.reconcile()
	Logf("access from other devices ON (PIN shown on this computer's page)")
	return l.State()
}

// Disable closes every listener and signs every device out.
func (l *LANAccess) Disable() LANState {
	l.mu.Lock()
	if l.enabled {
		close(l.stop)
		for _, srv := range l.listeners {
			srv.Close()
		}
		l.enabled, l.pin, l.listeners, l.errs, l.sessions = false, "", nil, nil, nil
		Logf("access from other devices OFF")
	}
	l.mu.Unlock()
	return l.State()
}

// State is the current state, PIN included: only this computer's page may see it.
func (l *LANAccess) State() LANState {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := LANState{Enabled: l.enabled, PIN: l.pin, URLs: []string{}, Devices: len(l.sessions), SavedPIN: l.savedPIN(false), CanSave: l.pinFile != ""}
	for a := range l.listeners {
		s.URLs = append(s.URLs, "http://"+a)
	}
	for a, e := range l.errs {
		s.Errors = append(s.Errors, a+": "+e)
	}
	sort.Strings(s.URLs)
	sort.Strings(s.Errors)
	return s
}

// reconcile listens on every current address and closes listeners on gone ones.
func (l *LANAccess) reconcile() {
	want := map[string]bool{}
	for _, i := range l.read() {
		ip := net.ParseIP(i.Address)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() && !l.loopOK {
			continue
		}
		want[net.JoinHostPort(i.Address, strconv.Itoa(l.port))] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled {
		return
	}
	for a, srv := range l.listeners {
		if !want[a] {
			srv.Close()
			delete(l.listeners, a)
			Logf("other devices: stopped listening on %s", a)
		}
	}
	for a := range l.errs {
		if !want[a] {
			delete(l.errs, a)
		}
	}
	for a := range want {
		if _, ok := l.listeners[a]; ok {
			continue
		}
		ln, err := net.Listen("tcp", a)
		if err != nil {
			l.errs[a] = err.Error()
			Warnf("other devices: cannot listen on %s: %v", a, err)
			continue
		}
		delete(l.errs, a)
		srv := &http.Server{Handler: l.guard(l.handler), ReadHeaderTimeout: 10 * time.Second}
		l.listeners[a] = srv
		Logf("other devices: listening on http://%s", a)
		go func() { _ = srv.Serve(ln) }()
	}
}

func newPIN() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand does not fail on a working system
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (l *LANAccess) signedIn(r *http.Request) bool {
	c, err := r.Cookie(lanCookie)
	if err != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	made, ok := l.sessions[c.Value]
	return ok && time.Since(made) < lanSessionLife
}

// guard is what another device's requests go through.
func (l *LANAccess) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		on := l.enabled
		l.mu.Unlock()
		if !on {
			http.Error(w, "Access from other devices is off.", http.StatusServiceUnavailable)
			return
		}
		// The address in Host must be the one this listener is on (or a .local name
		// for it). A website that points its own name at this computer would
		// otherwise be the same site as this page to the browser, and could sign in
		// with a PIN it got hold of and send changes.
		if !l.ownHost(r) {
			Warnf("other devices: refused %s %s for Host %q", r.Method, r.URL.Path, r.Host)
			http.Error(w, "Open this tool by its address, as shown on the computer running it.", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/pin" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
				Warnf("other devices: refused a PIN from Origin %q, Host %q", r.Header.Get("Origin"), r.Host)
				http.Error(w, `{"ok":false,"error":"Refused: not from this tool's page."}`, http.StatusForbidden)
				return
			}
			l.pinPage(w, r)
			return
		}
		// Turning access on or off, and seeing the PIN, is only for this computer.
		if r.URL.Path == "/api/lan" {
			http.Error(w, `{"ok":false,"error":"Only on the computer running the tool."}`, http.StatusForbidden)
			return
		}
		if !l.signedIn(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, `{"ok":false,"error":"Enter the PIN first."}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/pin?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		// Changes, and the live WebSocket, only from this tool's own page.
		if (r.Method != http.MethodGet && r.Method != http.MethodHead || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) && !sameOrigin(r) {
			Warnf("other devices: refused %s %s from Origin %q, Host %q", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, `{"ok":false,"error":"Refused: not from this tool's page."}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownHost: Host names the address this request arrived on, or a .local name
// (the Pi's own name on the network, which no outside website can take).
func (l *LANAccess) ownHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
		return a.IP.Equal(ip)
	}
	return false
}

var errLocked = errors.New("locked")

func (l *LANAccess) checkPIN(pin string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled {
		return "", errors.New("off")
	}
	if time.Now().Before(l.lockedTil) {
		return "", errLocked
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(pin)), []byte(l.pin)) != 1 {
		l.fails++
		if l.fails >= lanMaxFails {
			l.fails, l.lockedTil = 0, time.Now().Add(lanLockFor)
			Warnf("other devices: %d wrong PINs, PIN entry locked for %s", lanMaxFails, lanLockFor)
			return "", errLocked
		}
		return "", errors.New("wrong")
	}
	l.fails = 0
	for old, made := range l.sessions {
		if time.Since(made) >= lanSessionLife {
			delete(l.sessions, old)
		}
	}
	t := newToken()
	l.sessions[t] = time.Now()
	return t, nil
}

// pinPage is the sign-in page for another device. The page sends the PIN with
// fetch as JSON, not as a form: browsers flag a form submitted over plain http
// as insecure ("Not Secure", "the information you're about to submit is not
// secure"), and there is no form here for them to flag. The PIN never was
// sensitive beyond this show network; the session cookie is what matters. A
// plain form POST is still accepted (older pages, tests).
func (l *LANAccess) pinPage(w http.ResponseWriter, r *http.Request) {
	asJSON := strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
	var pin, next string
	if asJSON && r.Method == http.MethodPost {
		var body struct{ PIN, Next string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error":"Bad request."}`)
			return
		}
		pin, next = body.PIN, body.Next
	} else {
		pin, next = r.FormValue("pin"), r.FormValue("next")
	}
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		next = "/"
	}
	msg := ""
	if r.Method == http.MethodPost {
		token, err := l.checkPIN(pin)
		switch {
		case err == nil:
			Logf("other devices: %s signed in", r.RemoteAddr)
			http.SetCookie(w, &http.Cookie{Name: lanCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(lanSessionLife.Seconds())})
			if asJSON {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				b, _ := json.Marshal(map[string]any{"ok": true, "next": next})
				w.Write(b)
				return
			}
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		case errors.Is(err, errLocked):
			msg = "Too many wrong PINs. Try again in a minute."
		default:
			Warnf("other devices: wrong PIN from %s", r.RemoteAddr)
			msg = "That PIN is not right. It is on the computer running the tool."
		}
		if asJSON {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
			w.Write(b)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if msg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	nextJSON, _ := json.Marshal(next)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Enter PIN</title><style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0e0e0f;color:#d4d4d8;font:16px -apple-system,system-ui,sans-serif}
.box{background:#161618;border:1px solid #2a2a2e;border-radius:8px;padding:28px;width:300px;display:flex;flex-direction:column;gap:14px}
h1{font-size:18px;margin:0;font-weight:600}p{margin:0;color:#7c7c86;font-size:14px;line-height:1.5}
input{font:600 28px ui-monospace,Menlo,monospace;letter-spacing:.3em;text-align:center;background:#1d1d20;color:#f97316;border:1px solid #353539;border-radius:6px;padding:10px}
button{font:600 16px -apple-system,system-ui,sans-serif;padding:12px;border-radius:6px;border:none;background:#f97316;color:#000}
button:disabled{opacity:.5}.err{color:#ef4444;min-height:1.5em}</style></head><body>
<div class="box"><h1>Luminex tool</h1>
<p>Enter the PIN shown on the computer running the tool.</p>
<input id="pin" inputmode="numeric" pattern="[0-9]*" autocomplete="off" autocorrect="off" spellcheck="false" maxlength="6" autofocus aria-label="PIN">
<button id="go" type="button">Continue</button><p class="err" id="err">%s</p></div>
<script>
const next = %s, pin = document.getElementById('pin'), go = document.getElementById('go'), err = document.getElementById('err');
let busy = false;
async function send() {
  const v = pin.value.replace(/\D/g, '');
  if (busy || v.length !== 6) { if (!busy) err.textContent = 'The PIN has six digits.'; return; }
  busy = true; go.disabled = true; err.textContent = '';
  try {
    const r = await fetch('/pin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ pin: v, next }) });
    const d = await r.json().catch(() => ({}));
    if (d.ok) { location.replace(d.next || '/'); return; }
    err.textContent = d.error || ('Sign-in failed (' + r.status + ').');
    pin.value = ''; pin.focus();
  } catch (e) { err.textContent = 'Cannot reach the tool: ' + e.message; }
  busy = false; go.disabled = false;
}
pin.addEventListener('input', () => { pin.value = pin.value.replace(/\D/g, '').slice(0, 6); if (pin.value.length === 6) send(); });
pin.addEventListener('keydown', e => { if (e.key === 'Enter') send(); });
go.addEventListener('click', send);
</script></body></html>`, html.EscapeString(msg), nextJSON)
}

// ServeLANSetting answers GET and POST /api/lan for this computer's page: the
// state, {"enabled": bool} to turn access on or off, and {"pin": "123456"} to
// save a PIN ("" for a random one each time).
func (l *LANAccess) ServeLANSetting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		var body struct {
			Enabled *bool   `json:"enabled"`
			PIN     *string `json:"pin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Enabled == nil) == (body.PIN == nil) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": `send {"enabled": true|false} or {"pin": "123456"|""}`})
			return
		}
		switch {
		case body.PIN != nil:
			if err := l.SavePIN(strings.TrimSpace(*body.PIN)); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
				return
			}
		case *body.Enabled:
			l.Enable()
		default:
			l.Disable()
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "lan": l.State()})
}
