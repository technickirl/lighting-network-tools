package luminex

// The HTTP client every call to a device goes through: one transport, one log
// format, one place that honours DRY_RUN and DEVICE_PORT.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout  = 5 * time.Second
	maxResponseBody = 4 << 20 // a node's process blocks and IOs run to hundreds of KB
	maxLoggedBody   = 1000    // startup buffers can be several KB; a form or IP change is far less
	maxLoggedReply  = 400
)

// DryRun is true when DRY_RUN=1 asks that nothing be written to a device.
// Fetch refuses writes while it is set; reads still go out.
var DryRun = parseDryRun(os.Getenv("DRY_RUN"))

func parseDryRun(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ErrDryRun is what Fetch returns for a write it did not send.
var ErrDryRun = errors.New("dry run: not sent")

// Real devices always serve the API on port 80. DEVICE_PORT exists only so the
// test fakes can run without root. Read on first use, so a tool that does not
// use this client does not warn about it.
var devicePort = sync.OnceValue(func() string {
	p := os.Getenv("DEVICE_PORT")
	if p != "" {
		Warnf("DEVICE_PORT=%s set — talking to devices on that port instead of 80 (testing only)", p)
	}
	return portSuffix(p)
})

func portSuffix(p string) string {
	if p == "" {
		return ""
	}
	return ":" + p
}

// DeviceURL is the URL of an API path on a device.
func DeviceURL(ip, path string) string { return "http://" + ip + devicePort() + path }

// Auth is HTTP Basic credentials for a device; nil means none.
type Auth struct {
	Username string
	Password string
}

// String is what the log shows. It never contains the password.
func (a *Auth) String() string {
	if a == nil {
		return "no auth"
	}
	pw := "(blank)"
	if a.Password != "" {
		pw = "***"
	}
	return fmt.Sprintf("Basic %s:%s", a.Username, pw)
}

// Passwords a device has accepted, held in memory only and never logged, so a
// password entered for a job also works for the configure page and the other way
// round. Keyed by address: whoever moves a device remembers it at the new address.
var known = struct {
	sync.Mutex
	auth map[string]*Auth
}{auth: map[string]*Auth{}}

// RememberAuth records credentials a device at ip has accepted.
func RememberAuth(ip string, a *Auth) {
	if a == nil {
		return
	}
	known.Lock()
	defer known.Unlock()
	known.auth[ip] = a
}

// ForgetAllAuthForTests clears what RememberAuth has kept.
func ForgetAllAuthForTests() {
	known.Lock()
	defer known.Unlock()
	known.auth = map[string]*Auth{}
}

// KnownAuth returns the credentials last accepted at ip, or nil.
func KnownAuth(ip string) *Auth {
	known.Lock()
	defer known.Unlock()
	return known.auth[ip]
}

// Denied says whether a status means the device wants different credentials.
func Denied(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// Call is one request to a device.
type Call struct {
	Method      string
	URL         string
	ContentType string // sent only when set
	Accept      string // sent only when set
	Body        string
	Auth        *Auth
	Timeout     time.Duration // 0 means 5 s
	QuietReply  bool          // log only the size of a 2xx reply (for pollers, whose replies are large and constant)
}

// Response is what a device answered. Any HTTP status is a Response; an error
// from Fetch means there was no answer at all.
type Response struct {
	Status      int
	Text        string
	ContentType string
}

// dialDevice is replaced only by tests, to reach a stand-in at a device's address.
var dialDevice = (&net.Dialer{}).DialContext

// SetDeviceDialForTests sends every device connection through dial.
func SetDeviceDialForTests(dial func(ctx context.Context, network, addr string) (net.Conn, error)) {
	dialDevice = dial
}

var deviceClient = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialDevice(ctx, network, addr)
	}},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Fetch sends one request and logs it and its answer. While DryRun is set,
// anything but a GET is logged and not sent, and the error is ErrDryRun.
func Fetch(c Call) (*Response, error) {
	extra := ""
	if c.Body != "" {
		extra = " body=" + clip(c.Body, maxLoggedBody)
	}
	Logf("→ %s %s [%s]%s", c.Method, c.URL, c.Auth.String(), extra)
	if DryRun && c.Method != http.MethodGet {
		Logf("  DRY RUN — not sent")
		return nil, ErrDryRun
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var rdr io.Reader
	if c.Body != "" {
		rdr = strings.NewReader(c.Body)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, c.URL, rdr)
	if err != nil {
		return nil, err
	}
	if c.ContentType != "" {
		req.Header.Set("Content-Type", c.ContentType)
	}
	if c.Accept != "" {
		req.Header.Set("Accept", c.Accept)
	}
	if c.Auth != nil {
		req.SetBasicAuth(c.Auth.Username, c.Auth.Password)
	}

	t0 := time.Now()
	res, err := deviceClient.Do(req)
	if err != nil {
		msg := noAnswerReason(err, timeout)
		Logf("✗ %s %s (%dms): %s", c.Method, c.URL, time.Since(t0).Milliseconds(), msg)
		return nil, errors.New(msg)
	}
	defer res.Body.Close()
	// A device that sends its status and then cuts the reply short still answered:
	// callers decide on the status, so keep what arrived and only note the cut.
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBody))
	if err != nil {
		Logf("  reply cut short after %d bytes: %s", len(b), noAnswerReason(err, timeout))
	}
	text := string(b)
	if c.QuietReply && res.StatusCode >= 200 && res.StatusCode < 300 {
		Logf("← %d %s %s (%dms, %d bytes)", res.StatusCode, c.Method, c.URL, time.Since(t0).Milliseconds(), len(b))
	} else {
		Logf("← %d %s %s (%dms) %s", res.StatusCode, c.Method, c.URL, time.Since(t0).Milliseconds(), clip(strings.Join(strings.Fields(text), " "), maxLoggedReply))
	}
	return &Response{Status: res.StatusCode, Text: text, ContentType: res.Header.Get("Content-Type")}, nil
}

// noAnswerReason turns a transport error into the short reason operators see.
func noAnswerReason(err error, timeout time.Duration) string {
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "deadline exceeded") {
		return fmt.Sprintf("no reply within %gs", timeout.Seconds())
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
