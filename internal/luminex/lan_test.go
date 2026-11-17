package luminex

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func lanForTest(t *testing.T) (*LANAccess, string) {
	t.Helper()
	port := freePort(t)
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tool page") })
	h.HandleFunc("POST /api/do", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) })
	l := NewLANAccess(port, h)
	l.SetInterfacesForTests(func() []Iface { return []Iface{{Name: "lo0", Address: "127.0.0.1"}} })
	t.Cleanup(func() { l.Disable() })
	return l, "http://127.0.0.1:" + strconv.Itoa(port)
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}

func postPIN(t *testing.T, base, pin string) *http.Response {
	t.Helper()
	res, err := noRedirect.PostForm(base+"/pin", url.Values{"pin": {pin}, "next": {"/configure/"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// The PIN page sends the PIN with fetch as JSON (no form for a browser to
// flag as insecure over http): the right PIN gets the cookie and where to go,
// a wrong one a JSON error, and the page itself has no <form>.
func TestLANPINAsJSON(t *testing.T) {
	l, base := lanForTest(t)
	st := l.Enable()
	res, err := noRedirect.Get(base + "/pin?next=/configure/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(page), "<form") || !strings.Contains(string(page), `const next = "/configure/"`) {
		t.Errorf("PIN page: %s", page)
	}
	post := func(body string) (int, string, []*http.Cookie) {
		res, err := noRedirect.Post(base+"/pin", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Cookies()
	}
	if c, b, ck := post(`{"pin":"000000","next":"/configure/"}`); c != 401 || !strings.Contains(b, `"ok":false`) || len(ck) != 0 {
		t.Errorf("wrong PIN: %d %s %v", c, b, ck)
	}
	if c, b, _ := post(`{"pin":"`); c != 400 {
		t.Errorf("bad JSON: %d %s", c, b)
	}
	c, b, ck := post(`{"pin":"` + st.PIN + `","next":"//evil.example/"}`)
	if c != 200 || b != `{"next":"/","ok":true}` || len(ck) != 1 {
		t.Fatalf("right PIN: %d %s %v", c, b, ck)
	}
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.AddCookie(ck[0])
	res, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(got) != "tool page" {
		t.Errorf("with the cookie: %d %s", res.StatusCode, got)
	}
}

func TestLANAccessIsOffUntilTurnedOn(t *testing.T) {
	l, base := lanForTest(t)
	if _, err := noRedirect.Get(base + "/"); err == nil {
		t.Fatal("something answered before access was turned on")
	}
	if s := l.State(); s.Enabled || s.PIN != "" || len(s.URLs) != 0 {
		t.Errorf("state %+v", s)
	}
}

func TestLANAccessWithPIN(t *testing.T) {
	l, base := lanForTest(t)
	s := l.Enable()
	if !s.Enabled || len(s.PIN) != 6 || len(s.URLs) != 1 || s.URLs[0] != base {
		t.Fatalf("state %+v", s)
	}

	res, err := noRedirect.Get(base + "/configure/?ip=1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/pin?next=") {
		t.Errorf("page without PIN: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := noRedirect.Get(base + "/api/events"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("API without PIN: %d", res.StatusCode)
	}
	if res := postPIN(t, base, "000000x"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong PIN: %d", res.StatusCode)
	}
	res = postPIN(t, base, s.PIN)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/configure/" {
		t.Fatalf("right PIN: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == lanCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", cookie)
	}

	do := func(method, path, origin string) int {
		req, _ := http.NewRequest(method, base+path, nil)
		req.AddCookie(cookie)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := do("GET", "/", ""); c != 200 {
		t.Errorf("page with PIN: %d", c)
	}
	if c := do("POST", "/api/do", base); c != 200 {
		t.Errorf("change from the tool's page: %d", c)
	}
	if c := do("POST", "/api/do", "http://evil.example"); c != http.StatusForbidden {
		t.Errorf("change from another site: %d", c)
	}
	if c := do("GET", "/api/lan", ""); c != http.StatusForbidden {
		t.Errorf("setting from another device: %d", c)
	}
	if l.State().Devices != 1 {
		t.Errorf("devices %d", l.State().Devices)
	}

	// A website that points its own name at this computer is the same site as
	// its own page to a browser, so Host must be this listener's address.
	asHost := func(method, path, host string) int {
		req, _ := http.NewRequest(method, base+path, nil)
		req.AddCookie(cookie)
		req.Host = host
		req.Header.Set("Origin", "http://"+host)
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	port := base[strings.LastIndex(base, ":"):]
	for host, want := range map[string]int{"evil.example" + port: 403, "10.9.9.9" + port: 403, "commission.local" + port: 200} {
		if c := asHost("POST", "/api/do", host); c != want {
			t.Errorf("change sent to Host %s: %d, want %d", host, c, want)
		}
	}
	if c := asHost("GET", "/pin", "evil.example"+port); c != http.StatusForbidden {
		t.Errorf("PIN page for another Host: %d", c)
	}

	// A new PIN signs everyone out.
	if l.Enable(); do("GET", "/", "") != http.StatusSeeOther {
		t.Error("old session still works after a new PIN")
	}
	// Off closes the listener.
	l.Disable()
	if _, err := noRedirect.Get(base + "/"); err == nil {
		t.Error("still answering after turning off")
	}
}

func TestLANAccessLocksAfterWrongPINs(t *testing.T) {
	l, base := lanForTest(t)
	s := l.Enable()
	for i := 0; i < lanMaxFails; i++ {
		postPIN(t, base, "x")
	}
	if res := postPIN(t, base, s.PIN); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("right PIN while locked: %d", res.StatusCode)
	}
}

func TestLANSettingEndpoint(t *testing.T) {
	l, _ := lanForTest(t)
	srv := http.NewServeMux()
	srv.HandleFunc("/api/lan", l.ServeLANSetting)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, srv)
	defer ln.Close()
	res, err := http.Post("http://"+ln.Addr().String()+"/api/lan", "application/json", strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), `"enabled":true`) || !strings.Contains(string(b), `"pin":"`) {
		t.Errorf("%d %s", res.StatusCode, b)
	}
}

func TestLANAccessSavedPIN(t *testing.T) {
	l, base := lanForTest(t)
	file := t.TempDir() + "/sub/lan-pin.json"
	if err := l.SavePIN("123456"); err == nil {
		t.Error("saved a PIN with no PIN file")
	}
	l.SetPINFile(file)
	for _, bad := range []string{"12345", "1234567", "12345a", " 23456"} {
		if err := l.SavePIN(bad); err == nil {
			t.Errorf("saved %q", bad)
		}
	}
	if err := l.SavePIN("482916"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("PIN file %v %v", fi, err)
	}
	if s := l.State(); s.SavedPIN != "482916" || !s.CanSave || s.Enabled {
		t.Errorf("state after save %+v", s)
	}
	if s := l.Enable(); s.PIN != "482916" {
		t.Errorf("turned on with %q, not the saved PIN", s.PIN)
	}
	if res := postPIN(t, base, "482916"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("saved PIN refused: %d", res.StatusCode)
	}
	if l.State().Devices != 1 {
		t.Fatal("no session")
	}
	// Saving another PIN while on takes over now and signs devices out.
	if err := l.SavePIN("777001"); err != nil {
		t.Fatal(err)
	}
	if s := l.State(); s.PIN != "777001" || s.Devices != 0 {
		t.Errorf("after a new saved PIN %+v", s)
	}
	// New PIN while on is random for now; the saved one comes back next time.
	if s := l.Enable(); s.PIN == "777001" || len(s.PIN) != 6 {
		t.Errorf("New PIN gave %q", s.PIN)
	}
	l.Disable()
	if s := l.Enable(); s.PIN != "777001" {
		t.Errorf("turned on again with %q", s.PIN)
	}
	l.Disable()
	if err := l.SavePIN(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("PIN file still there: %v", err)
	}
	if err := l.SavePIN(""); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	if s := l.Enable(); s.PIN == "777001" || s.SavedPIN != "" {
		t.Errorf("after clearing %+v", s)
	}
	// A broken file is ignored.
	os.MkdirAll(filepath.Dir(file), 0o700)
	os.WriteFile(file, []byte(`{"pin":"12"}`), 0o600)
	if s := l.State(); s.SavedPIN != "" {
		t.Errorf("broken file gave %q", s.SavedPIN)
	}
}

func TestLANSettingSavesPIN(t *testing.T) {
	l, _ := lanForTest(t)
	l.SetPINFile(t.TempDir() + "/lan-pin.json")
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		l.ServeLANSetting(w, httptest.NewRequest("POST", "/api/lan", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if c, b := post(`{"pin":"135790"}`); c != 200 || !strings.Contains(b, `"savedPin":"135790"`) {
		t.Errorf("save: %d %s", c, b)
	}
	if c, _ := post(`{"pin":"abc"}`); c != 400 {
		t.Errorf("bad PIN: %d", c)
	}
	if c, _ := post(`{"pin":"135790","enabled":true}`); c != 400 {
		t.Errorf("both at once: %d", c)
	}
	if c, b := post(`{"enabled":true}`); c != 200 || !strings.Contains(b, `"pin":"135790"`) {
		t.Errorf("on: %d %s", c, b)
	}
}
