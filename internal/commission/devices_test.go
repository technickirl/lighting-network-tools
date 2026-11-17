package commission

// What IP Setup actually puts on the wire, checked against a local server. No
// test here talks to a real device.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

type seenReq struct {
	Method, Path, ContentType, Body, Authorization string
}

// wire is a fake device that records every request and answers through reply.
type wire struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []seenReq
}

func newWire(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *wire {
	t.Helper()
	f := &wire{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seenReq{r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b), r.Header.Get("Authorization")})
		f.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *wire) addr() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *wire) requests() []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenReq(nil), f.seen...)
}

func answer(status int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }
}

// Basic admin: and Basic admin:pw, as they appear in the header.
const (
	basicAdminBlank = "Basic YWRtaW46"
	basicAdminPw    = "Basic YWRtaW46cHc="
)

func TestWritesOnTheWire(t *testing.T) {
	plan := IPPlan{IP: "192.168.12.3", Netmask: "255.255.0.0", Gateway: "0.0.0.0"}
	gen1IP := "if_ct=1&if_vid_0=1&if_addr_0=192.168.12.3&if_mask_0=16&if_dhcp_0=0&rt_ct=2&rt_del_0=1&rt_net_0=0.0.0.0&rt_mask_0=0&rt_dest_0=0.0.0.0&rt_net_1=0.0.0.0&rt_mask_1=0&rt_dest_1=0.0.0.0"
	cases := []struct {
		name  string
		kind  string
		build func(d luminex.Device) (*request, request)
		want  []seenReq
	}{
		{"luminode IP", "luminode", func(d luminex.Device) (*request, request) { return ipRequests(d, plan) },
			[]seenReq{{"PUT", "/api/ipsettings", "application/json", `{"ipaddress":"192.168.12.3","netmask":"255.255.0.0","gateway":"0.0.0.0"}`, ""}}},
		{"gen2 IP", "gen2", func(d luminex.Device) (*request, request) { return ipRequests(d, plan) },
			[]seenReq{{"PUT", "/api/ip_settings", "application/json", `{"mode":"static","ip_address":"192.168.12.3","prefix_length":16,"default_gateway":"0.0.0.0"}`, ""}}},
		{"gen1 IP, preflight first, fields in order", "gen1", func(d luminex.Device) (*request, request) { return ipRequests(d, plan) },
			[]seenReq{{"GET", "/config/ip2_ll_ip", "", "", ""}, {"POST", "/config/ip2_config", "application/x-www-form-urlencoded", gen1IP, ""}}},
		{"luminode reset", "luminode", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, false, false) },
			[]seenReq{{"POST", "/api/reset", "application/json", `{"keep_ip_settings":false,"keep_user_profiles":false}`, ""}}},
		{"luminode reset keeping profiles", "luminode", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, true, false) },
			[]seenReq{{"POST", "/api/reset", "application/json", `{"keep_ip_settings":false,"keep_user_profiles":true}`, ""}}},
		{"gen2 reset", "gen2", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, false, false) },
			[]seenReq{{"PUT", "/api/reset", "application/json", `{"keep_ip":false,"keep_profiles":false,"wait":0}`, ""}}},
		{"gen1 reset", "gen1", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, false, false) },
			[]seenReq{{"POST", "/config/misc", "application/x-www-form-urlencoded", "factory_full=yes&clear_profiles=yes", ""}}},
		{"luminode reset keeping IP and profiles", "luminode", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, true, true) },
			[]seenReq{{"POST", "/api/reset", "application/json", `{"keep_ip_settings":true,"keep_user_profiles":true}`, ""}}},
		{"gen2 reset keeping IP", "gen2", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, false, true) },
			[]seenReq{{"PUT", "/api/reset", "application/json", `{"keep_ip":true,"keep_profiles":false,"wait":0}`, ""}}},
		{"gen1 reset keeping IP", "gen1", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, false, true) },
			[]seenReq{{"POST", "/config/misc", "application/x-www-form-urlencoded", "factory=yes&clear_profiles=yes", ""}}},
		{"gen1 reset keeping IP and profiles", "gen1", func(d luminex.Device) (*request, request) { return nil, resetRequest(d, true, true) },
			[]seenReq{{"POST", "/config/misc", "application/x-www-form-urlencoded", "factory=yes", ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWire(t, answer(202))
			d := luminex.Device{IP: f.addr(), APIKind: c.kind}
			pre, rq := c.build(d)
			if res := sendWrite(d, pre, rq, nil); res.Outcome != "sent" {
				t.Fatalf("outcome = %+v", res)
			}
			got := f.requests()
			if len(got) != len(c.want) {
				t.Fatalf("requests = %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("request %d\n got %+v\nwant %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestSendWriteOutcomes(t *testing.T) {
	d := func(f *wire) luminex.Device { return luminex.Device{IP: f.addr(), APIKind: "luminode"} }
	rq := func(f *wire) request { return resetRequest(d(f), false, false) }

	t.Run("rejected password", func(t *testing.T) {
		f := newWire(t, answer(401))
		if res := sendWrite(d(f), nil, rq(f), nil); res.Outcome != "auth" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("error status keeps the device's words", func(t *testing.T) {
		f := newWire(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", 500) })
		res := sendWrite(d(f), nil, rq(f), nil)
		if res.Outcome != "failed" || !strings.Contains(res.Reason, "HTTP 500") || !strings.Contains(res.Reason, "busy") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("dropped connection means probably rebooting", func(t *testing.T) {
		f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				c.Close()
			}
		})
		if res := sendWrite(d(f), nil, rq(f), nil); res.Outcome != "dropped" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("status then a cut-short reply still counts as the status", func(t *testing.T) {
		f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				c.Close()
			}
		})
		if res := sendWrite(d(f), nil, rq(f), nil); res.Outcome != "sent" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("gen1 retries the alternate path on 404", func(t *testing.T) {
		f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/config/misc" {
				w.WriteHeader(404)
			}
		})
		g := luminex.Device{IP: f.addr(), APIKind: "gen1"}
		if res := sendWrite(g, nil, resetRequest(g, false, false), nil); res.Outcome != "sent" {
			t.Fatalf("%+v", res)
		}
		got := f.requests()
		if len(got) != 2 || got[1].Path != "/confg/misc" {
			t.Errorf("requests = %+v", got)
		}
	})
	t.Run("dry run sends nothing, preflight included", func(t *testing.T) {
		setDry(t, true)
		f := newWire(t, answer(200))
		g := luminex.Device{IP: f.addr(), APIKind: "gen1"}
		pre, write := ipRequests(g, IPPlan{IP: "192.168.11.3", Netmask: "255.255.0.0", Gateway: "0.0.0.0"})
		res := sendWrite(g, pre, write, &Auth{Username: "admin"})
		if res.Outcome != "dry" || !strings.Contains(res.Request, "/config/ip2_config") {
			t.Errorf("%+v", res)
		}
		if got := f.requests(); len(got) != 0 {
			t.Errorf("sent under dry run: %+v", got)
		}
	})
}

func TestFindAuth(t *testing.T) {
	// Accepts only the header it is told to.
	only := func(want string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != want {
				w.WriteHeader(401)
			}
		}
	}
	t.Run("open device needs nothing", func(t *testing.T) {
		f := newWire(t, answer(200))
		a, needs, err := findAuth(luminex.Device{IP: f.addr(), APIKind: "luminode"}, nil)
		if a != nil || needs || err != nil {
			t.Errorf("auth=%v needs=%v err=%v", a, needs, err)
		}
		if got := f.requests(); len(got) != 1 || got[0].Path != "/api/deviceinfo" {
			t.Errorf("requests = %+v", got)
		}
	})
	t.Run("falls back to admin with a blank password", func(t *testing.T) {
		f := newWire(t, only(basicAdminBlank))
		a, needs, err := findAuth(luminex.Device{IP: f.addr(), APIKind: "gen2"}, nil)
		if a == nil || a.Username != "admin" || a.Password != "" || needs || err != nil {
			t.Errorf("auth=%v needs=%v err=%v", a, needs, err)
		}
		if got := f.requests(); len(got) != 2 || got[0].Path != "/api/device" || got[0].Authorization != "" {
			t.Errorf("requests = %+v", got)
		}
	})
	t.Run("gen1 never asks without credentials", func(t *testing.T) {
		f := newWire(t, only(basicAdminBlank))
		a, _, err := findAuth(luminex.Device{IP: f.addr(), APIKind: "gen1"}, nil)
		if a == nil || err != nil {
			t.Fatalf("auth=%v err=%v", a, err)
		}
		if got := f.requests(); len(got) != 1 || got[0].Path != "/stat/ports" || got[0].Authorization != basicAdminBlank {
			t.Errorf("requests = %+v", got)
		}
	})
	t.Run("password device asks, then takes the password", func(t *testing.T) {
		f := newWire(t, only(basicAdminPw))
		d := luminex.Device{IP: f.addr(), APIKind: "luminode"}
		if a, needs, err := findAuth(d, nil); a != nil || !needs || err != nil {
			t.Errorf("no password: auth=%v needs=%v err=%v", a, needs, err)
		}
		user := &Auth{Username: "admin", Password: "pw"}
		if a, needs, err := findAuth(d, user); a != user || needs || err != nil {
			t.Errorf("with password: auth=%v needs=%v err=%v", a, needs, err)
		}
	})
	t.Run("no answer is an error", func(t *testing.T) {
		f := newWire(t, answer(200))
		addr := f.addr()
		f.srv.Close()
		if _, _, err := findAuth(luminex.Device{IP: addr, APIKind: "luminode"}, nil); err == nil {
			t.Error("closed server gave no error")
		}
	})
}

func TestIsUp(t *testing.T) {
	f := newWire(t, answer(401)) // any answer means it is up, even a refusal
	d := luminex.Device{IP: f.addr(), APIKind: "gen2"}
	if !isUp(d, nil) {
		t.Error("device answering 401 reported down")
	}
	f.srv.Close()
	if isUp(d, nil) {
		t.Error("closed server reported up")
	}
	if isUp(luminex.Device{IP: "127.0.0.1", APIKind: ""}, nil) {
		t.Error("unknown kind reported up")
	}
}
