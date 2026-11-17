package nodeconfig

// The page and the API as a tool mounts them. No test here talks to a device.

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

func mounted(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Mount(mux, Options{})
	mux.Handle("/", http.FileServer(http.FS(Assets())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPageLoadsItsScriptAndStyles(t *testing.T) {
	srv := mounted(t)
	get := func(path string) string {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, res.StatusCode)
		}
		return string(b)
	}
	page := get("/")
	for _, want := range []string{`href="nodeconfig.css"`, `src="nodeconfig.js"`, `data-tab="processblocks"`, `id="btn-connect"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page has no %s", want)
		}
	}
	if !strings.Contains(get("/nodeconfig.js"), "function renderProcessBlocks") {
		t.Error("script missing renderProcessBlocks")
	}
	if !strings.Contains(get("/nodeconfig.css"), "--amber") {
		t.Error("styles missing --amber")
	}
	if _, err := fs.Stat(Assets(), "index.html"); err != nil {
		t.Error(err)
	}
}

func TestBadDeviceAddressIsRefused(t *testing.T) {
	srv := mounted(t)
	res, err := http.Get(srv.URL + "/api/node/bad_host!/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 || body.OK || !strings.Contains(body.Error, "bad device address") {
		t.Errorf("%d %+v", res.StatusCode, body)
	}
}

func TestLiveSocketAnswersSubscribe(t *testing.T) {
	srv := mounted(t)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/live"
	conn, err := websocket.Dial(url, "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var reply struct {
		Type string `json:"type"`
		IP   string `json:"ip"`
	}
	// A bad address is ignored; closing before the first 3 s tick means the
	// poller never contacts anything.
	if err := websocket.JSON.Send(conn, map[string]string{"type": "subscribe", "ip": "bad_host!"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Send(conn, map[string]string{"type": "subscribe", "ip": "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Receive(conn, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != "subscribed" || reply.IP != "127.0.0.1" {
		t.Errorf("reply = %+v", reply)
	}
	if err := websocket.JSON.Send(conn, map[string]string{"type": "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Receive(conn, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != "unsubscribed" {
		t.Errorf("reply = %+v", reply)
	}
}

// A page asks the app's version when its live link reconnects, and reloads when it changed.
func TestAppVersion(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, Options{Version: "9.8.7"})
	defer func() { options = Options{} }()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/app-version")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || !out.OK || out.Version != "9.8.7" {
		t.Fatalf("app-version: %d %+v %v", res.StatusCode, out, err)
	}
}
