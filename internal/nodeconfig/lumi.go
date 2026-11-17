package nodeconfig

// LumiNode WebAPI (v2.7.2) calls, on top of the shared device client: it logs
// every request and response and, under DRY_RUN=1, does not send writes
// (POST/PUT/PATCH/DELETE); reads still go to the device.

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"net/http"
	"strings"
	"time"
)

type obj = map[string]any

const deviceTimeout = 5 * time.Second

// errNeedsPassword means the device refused the credentials known for it.
var errNeedsPassword = errors.New("this device has a password set")

func lumiURL(ip, apiPath string) string { return luminex.DeviceURL(ip, apiPath) }

// lumiDo sends one request to a device. body may be nil (no body), a
// json.RawMessage (forwarded verbatim) or any value (marshalled).
// Returns the response body (nil when empty) or an error that includes the
// status and the device's error text.
func lumiDo(method, ip, apiPath string, body any) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		switch b := body.(type) {
		case json.RawMessage:
			payload = b
		case []byte:
			payload = b
		default:
			var err error
			if payload, err = json.Marshal(b); err != nil {
				return nil, err
			}
		}
	}
	call := luminex.Call{Method: method, URL: lumiURL(ip, apiPath), Accept: "application/json", Timeout: deviceTimeout, QuietReply: true}
	if payload != nil {
		call.Body, call.ContentType = string(payload), "application/json"
	}
	call.Auth = luminex.KnownAuth(ip)
	res, err := luminex.Fetch(call)
	if err == nil && luminex.Denied(res.Status) && call.Auth == nil {
		// Same order as a job: no credentials, then admin with a blank password. A
		// refused request changed nothing, so sending a write again is safe.
		call.Auth = &luminex.Auth{Username: "admin"}
		if res, err = luminex.Fetch(call); err == nil && !luminex.Denied(res.Status) {
			luminex.RememberAuth(ip, call.Auth)
		}
	}
	if errors.Is(err, luminex.ErrDryRun) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v", method, apiPath, err)
	}
	if luminex.Denied(res.Status) {
		return nil, errNeedsPassword
	}
	if res.Status < 200 || res.Status > 299 {
		msg := strings.TrimSpace(res.Text)
		if msg == "" {
			return nil, fmt.Errorf("%d %s (%s)", res.Status, http.StatusText(res.Status), apiPath)
		}
		return nil, fmt.Errorf("%d %s: %s", res.Status, http.StatusText(res.Status), truncate(msg, 200))
	}
	if strings.TrimSpace(res.Text) == "" {
		return nil, nil
	}
	return json.RawMessage(res.Text), nil
}

func lumiGet(ip, apiPath string) (json.RawMessage, error) {
	return lumiDo(http.MethodGet, ip, apiPath, nil)
}

// lumiGetInto GETs and decodes into v.
func lumiGetInto(ip, apiPath string, v any) error {
	raw, err := lumiGet(ip, apiPath)
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("empty response from %s", apiPath)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("bad JSON from %s: %v", apiPath, err)
	}
	return nil
}

func lumiObjects(ip, apiPath string) ([]obj, error) {
	var list []obj
	if err := lumiGetInto(ip, apiPath, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func lumiObject(ip, apiPath string) (obj, error) {
	var o obj
	if err := lumiGetInto(ip, apiPath, &o); err != nil {
		return nil, err
	}
	return o, nil
}

// createdIndex reads {"index": n} from a POST /api/IO response. -1 under DRY_RUN.
func createdIndex(raw json.RawMessage) int {
	var r struct {
		Index *int `json:"index"`
	}
	if raw == nil || json.Unmarshal(raw, &r) != nil || r.Index == nil {
		return -1
	}
	return *r.Index
}

// truncate cuts s to n characters, on a character boundary, so a device
// message with an accented or non-Latin letter is never cut mid-rune.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
