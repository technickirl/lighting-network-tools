package luminex

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func setDryRun(t *testing.T, v bool) {
	t.Helper()
	old := DryRun
	DryRun = v
	t.Cleanup(func() { DryRun = old })
}

func TestFetchSendsOnlyTheHeadersItIsGiven(t *testing.T) {
	var got *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	res, err := Fetch(Call{Method: "PUT", URL: srv.URL + "/api/x", ContentType: "application/json", Accept: "application/json", Body: `{"a":1}`, Auth: &Auth{"admin", "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 202 || res.Text != `{"ok":true}` || res.ContentType != "application/json" {
		t.Errorf("response = %+v", res)
	}
	if got.Method != "PUT" || got.URL.Path != "/api/x" || gotBody != `{"a":1}` {
		t.Errorf("request = %s %s %q", got.Method, got.URL.Path, gotBody)
	}
	if u, p, ok := got.BasicAuth(); !ok || u != "admin" || p != "pw" {
		t.Errorf("auth = %q %q %v", u, p, ok)
	}
	if got.Header.Get("Accept") != "application/json" || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", got.Header)
	}

	// A plain probe carries none of them: GigaCore Gen1 and the LumiNode doc page
	// both care which headers arrive.
	if _, err = Fetch(Call{Method: "GET", URL: srv.URL + "/probe"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := got.BasicAuth(); ok {
		t.Error("nil Auth sent an Authorization header")
	}
	if got.Header.Get("Accept") != "" || got.Header.Get("Content-Type") != "" {
		t.Errorf("unset headers were sent: %v", got.Header)
	}
}

func TestBlankPasswordStillSendsAuthorization(t *testing.T) {
	// GigaCore Gen1 rejects a request with no Authorization header at all.
	var has bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, has = r.BasicAuth()
	}))
	defer srv.Close()
	if _, err := Fetch(Call{Method: "GET", URL: srv.URL, Auth: &Auth{Username: "admin"}}); err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("admin with a blank password sent no Authorization header")
	}
}

func TestDryRunBlocksWritesButNotReads(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	setDryRun(t, true)

	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		res, err := Fetch(Call{Method: m, URL: srv.URL, Body: "{}"})
		if !errors.Is(err, ErrDryRun) || res != nil {
			t.Errorf("%s under DryRun: res=%v err=%v", m, res, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d writes reached the device under DryRun", n)
	}
	if _, err := Fetch(Call{Method: "GET", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Error("GET did not reach the device under DryRun")
	}
}

func TestNonSuccessStatusIsAResponseNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 401)
	}))
	defer srv.Close()
	res, err := Fetch(Call{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 401 || strings.TrimSpace(res.Text) != "nope" {
		t.Errorf("response = %+v", res)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	res, err := Fetch(Call{Method: "POST", URL: srv.URL + "/api/reset"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusFound || followed.Load() {
		t.Errorf("status %d, followed %v", res.Status, followed.Load())
	}
}

func TestTimeoutSaysSo(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	_, err := Fetch(Call{Method: "GET", URL: srv.URL, Timeout: 50 * time.Millisecond})
	if err == nil || err.Error() != "no reply within 0.05s" {
		t.Errorf("err = %v", err)
	}
}

func TestDroppedConnectionIsAnError(t *testing.T) {
	// A GigaCore Gen1 drops the connection when a config POST succeeds; callers
	// tell that apart from an HTTP failure by getting an error here.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	if res, err := Fetch(Call{Method: "POST", URL: "http://" + ln.Addr().String() + "/config/misc", Body: "x=1"}); err == nil {
		t.Errorf("dropped connection returned %+v, no error", res)
	}
}

func TestAuthStringNeverShowsThePassword(t *testing.T) {
	for _, c := range []struct {
		a    *Auth
		want string
	}{
		{nil, "no auth"},
		{&Auth{"admin", ""}, "Basic admin:(blank)"},
		{&Auth{"admin", "hunter2"}, "Basic admin:***"},
	} {
		if got := c.a.String(); got != c.want {
			t.Errorf("%v.String() = %q, want %q", c.a, got, c.want)
		}
	}
}

func TestParseDryRun(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "no": false, "1": true, "true": true, "TRUE": true, "yes": true} {
		if got := parseDryRun(v); got != want {
			t.Errorf("parseDryRun(%q) = %v", v, got)
		}
	}
}

func TestPortSuffix(t *testing.T) {
	if portSuffix("") != "" || portSuffix("8081") != ":8081" {
		t.Errorf("portSuffix: %q %q", portSuffix(""), portSuffix("8081"))
	}
}

func TestReachabilityOfThisComputersOwnAddress(t *testing.T) {
	ifaces := LocalInterfaces()
	if len(ifaces) == 0 {
		t.Skip("no network interface here")
	}
	own := ifaces[0]
	t.Setenv("DEVICE_PORT", "")
	r := Reachability(own.Address, own.Prefix)
	if r.Reachable || !strings.Contains(r.Reason, "same address as this computer") || !strings.Contains(r.Reason, own.Name) {
		t.Errorf("a device on this computer's address cannot be reached: %+v", r)
	}
	t.Setenv("DEVICE_PORT", "8080") // the test fakes answer on this computer's own address
	if r := Reachability(own.Address, own.Prefix); !r.Reachable {
		t.Errorf("the fakes should stay reachable: %+v", r)
	}
	if got := OwnAddress("192.0.2.1", []Iface{{Name: "en11", Address: "192.168.1.56"}}); got != "" {
		t.Errorf("not our address: %q", got)
	}
}

func TestLocalOnly(t *testing.T) {
	h := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method, host, origin string
		want                 int
	}{
		{"POST", "localhost:3800", "http://localhost:3800", 204},
		{"POST", "127.0.0.1:3800", "http://127.0.0.1:3800", 204},
		{"POST", "[::1]:3800", "http://[::1]:3800", 204},
		{"POST", "localhost:3800", "", 204},                     // a script or test with no Origin
		{"POST", "localhost:3800", "https://evil.example", 403}, // another website
		{"POST", "localhost:3800", "http://localhost:9999", 403},
		{"POST", "localhost:3800", "null", 403}, // a sandboxed page
		{"POST", "evil.example:3800", "", 403},  // a DNS trick
		{"PUT", "localhost:3800", "http://evil.test", 403},
		{"GET", "localhost:3800", "https://evil.example", 204}, // reads change nothing, and that site cannot see the answer
		{"GET", "evil.example:3800", "", 403},                  // a DNS trick could read the answer (the PIN)
		{"GET", "192.168.1.20:3800", "", 403},
		{"WS", "localhost:3800", "https://evil.example", 403}, // another website listening to the live WebSocket
		{"WS", "localhost:3800", "http://localhost:3800", 204},
	}
	for _, c := range cases {
		method := c.method
		if method == "WS" {
			method = "GET"
		}
		r := httptest.NewRequest(method, "/api/settings", nil)
		if c.method == "WS" {
			r.Header.Set("Upgrade", "websocket")
		}
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s host=%s origin=%q: got %d, want %d", c.method, c.host, c.origin, w.Code, c.want)
		}
	}
}

func TestLinkLocalFromMAC(t *testing.T) {
	for mac, want := range map[string]string{
		"d0:69:9e:7d:59:4e": "fe80::d269:9eff:fe7d:594e",
		"d0:69:9e:b2:00:7f": "fe80::d269:9eff:feb2:7f", // leading zeros are dropped
		"d0:69:9e:63:0e:a4": "fe80::d269:9eff:fe63:ea4",
		"not a mac":         "",
		"":                  "",
	} {
		if got := LinkLocalFromMAC(mac); got != want {
			t.Errorf("%q: got %q, want %q", mac, got, want)
		}
	}
}

// A reset that never left this computer must not be mistaken for one the device accepted and then went quiet on.
func TestNoAnswerSaysWhetherTheRequestWasSent(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	if _, err := Fetch(Call{Method: "POST", URL: url + "/api/reset", Body: "{}"}); !NotSent(err) {
		t.Errorf("refused connection: NotSent = false (%v)", err)
	}

	drop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
			c.Close()
		}
	}))
	defer drop.Close()
	_, err := Fetch(Call{Method: "POST", URL: drop.URL + "/api/reset", Body: "{}"})
	if err == nil || NotSent(err) {
		t.Errorf("connection dropped after the request: err = %v, NotSent = %v", err, NotSent(err))
	}
	if NotSent(nil) || NotSent(ErrDryRun) {
		t.Error("NotSent is only for a Fetch that got no answer")
	}
}
