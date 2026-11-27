package commission

// Device-wide settings: name, LED brightness, identify, and turning the web password off. (SNMP on/off was removed: it
// reboots the switch. The Gen1 calls are kept in CLAUDE.md.)
//
//   GigaCore Gen1 (read off the web UI of real GigaCore 10/12 v3.1.5 switches, 5 Nov 2026; GET only so far):
//     name       GET /config/switchlegend → "name,descr(url-escaped),id,serial,rev,ass,MAC";
//                POST /config/switchlegend sw_name (max 16) sw_id sw_descr, then POST /config/icfg_conf_save save=1
//     LED        GET /config/lmx_led → "2|"; POST /config/lmx_led brightness=0..4, then save
//     identify   POST /config/lmx wink=1
//   GigaCore Gen2 (WebApi 1.5.0 spec only, NOT yet tried on a real switch):
//     name PUT /api/device/name "text"; LED PUT /api/led/brightness N;
//     identify PUT /api/identify {"duration":9}; web password off PUT /api/security/enabled false (spec: covers the
//     web page and API calls)
//   GigaCore Gen1 has no call to turn the password off (only POST /config/passwd to change it), so it is skipped.
//
//   LumiNode / LumiCore (name, web password off and identify; spec v2.9.1, read off real LumiNode 4/12 v2.8.1, 5 Nov 2026):
//     identify   POST /api/identify {} (204; the same call the Configure page uses)
//     name       PUT /api/deviceinfo via nameRequest (devices.go): ID and colors read first and sent back, short_name
//                max 17, printable ASCII; read back from GET /api/deviceinfo
//     web password off  PUT /api/deviceinfo/auth {"web_auth_enabled":false}; read back from GET /api/deviceinfo/auth
//   Only "off" is offered: turning it on needs a password to be set, which this page does not handle.
//
// DRY_RUN=1 logs the writes instead of sending them (sendWrite). Every write is read back afterwards.

import (
	"encoding/json"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var settingStatusLabel = map[string]string{
	"queued":   "Queued",
	"checking": "Checking…",
	"sending":  "Sending…",
	"done":     "Done ✓",
	"failed":   "Failed",
	"skipped":  "Skipped",
	"dry_run":  "Dry run (not sent)",
}

type SettingsItem struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Label  string         `json:"label"`
	Detail string         `json:"detail"`
	Value  string         `json:"value"`
	Device luminex.Device `json:"device"`
}

type SettingsSnapshot struct {
	ID     string         `json:"id"`
	Action string         `json:"action"`
	DryRun bool           `json:"dryRun"`
	Done   bool           `json:"done"`
	Items  []SettingsItem `json:"items"`
}

type SettingsRun struct {
	mu     sync.Mutex
	id     string
	action string
	items  []*SettingsItem
	done   bool
}

func (r *SettingsRun) snapshot() *SettingsSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &SettingsSnapshot{ID: r.id, Action: r.action, DryRun: luminex.DryRun, Done: r.done}
	for _, it := range r.items {
		s.Items = append(s.Items, *it)
	}
	return s
}

func (r *SettingsRun) set(it *SettingsItem, status, detail string) {
	r.mu.Lock()
	it.Status, it.Label, it.Detail = status, settingStatusLabel[status], detail
	r.mu.Unlock()
	if detail != "" {
		luminex.Logf("[%s] %s: %s — %s", it.Device.Serial, r.action, it.Label, detail)
	} else {
		luminex.Logf("[%s] %s: %s", it.Device.Serial, r.action, it.Label)
	}
	notify()
}

func (r *SettingsRun) checkDone() {
	r.mu.Lock()
	for _, it := range r.items {
		switch it.Status {
		case "done", "failed", "dry_run", "skipped":
		default:
			r.mu.Unlock()
			return
		}
	}
	r.done = true
	r.mu.Unlock()
	luminex.Logf("settings run %s (%s) finished", r.id, r.action)
	notify()
}

func (r *SettingsRun) start() {
	luminex.Logf("Settings run %s: %s on %d device(s)%s", r.id, r.action, len(r.items), map[bool]string{true: " (DRY RUN)"}[luminex.DryRun])
	queue := make(chan *SettingsItem, len(r.items))
	for _, it := range r.items {
		queue <- it
	}
	close(queue)
	var wg sync.WaitGroup
	for w := 0; w < min(concurrency, len(r.items)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range queue {
				r.runOne(it)
			}
		}()
	}
	wg.Wait()
	r.checkDone()
}

func (r *SettingsRun) runOne(it *SettingsItem) {
	d := it.Device
	switch {
	case d.Family == "gigacore" && (d.APIKind == "gen1" || d.APIKind == "gen2"):
	case (d.Family == "luminode" || d.Family == "lumicore") && d.APIKind == "luminode":
		if r.action != "name" && r.action != "webauth" && r.action != "identify" {
			r.set(it, "failed", "Only name, web password and Identify work on a LumiNode here.")
			return
		}
	default:
		r.set(it, "failed", "Not supported here.")
		return
	}
	if r.action == "webauth" && d.APIKind == "gen1" {
		r.set(it, "skipped", "Gen1 cannot turn the web password off.")
		return
	}
	if why := checkValue(r.action, d.APIKind, it.Value); why != "" {
		r.set(it, "failed", why)
		return
	}
	if rc := reachability(d.IP, d.Prefix); !rc.Reachable && d.LinkRoute == "" {
		r.set(it, "failed", "Unreachable. "+rc.Reason+" "+rc.Fix)
		return
	}
	r.set(it, "checking", "Checking it answers…")
	auth, needsPw, err := findAuth(d, nil)
	if err != nil {
		r.set(it, "failed", noAnswerText(d, err))
		return
	}
	if needsPw {
		if d.APIKind == "luminode" {
			r.set(it, "failed", "Password set. Enter it on its Configure page, then try again.")
		} else {
			r.set(it, "failed", "Password set; not handled here.")
		}
		return
	}
	r.set(it, "sending", "")
	status, detail := applySetting(d, r.action, it.Value, auth)
	r.set(it, status, detail)
}

var gen2NameOK = regexp.MustCompile(`^[A-Za-z0-9\-_ ]*$`)

// checkValue returns why a value cannot be used, or "".
func checkValue(action, kind, value string) string {
	switch action {
	case "name":
		limit := 16
		switch kind {
		case "gen2":
			limit = 64
		case "luminode":
			limit = 17
		}
		switch {
		case strings.TrimSpace(value) == "":
			return "The name is empty."
		case len(value) > limit:
			return fmt.Sprintf("%q is %d characters; this switch allows %d.", value, len(value), limit)
		case strings.ContainsAny(value, ",%|") || strings.ContainsFunc(value, func(c rune) bool { return c < 0x20 || c > 0x7e }):
			return "Names: plain keyboard characters only, not , % |"
		case kind == "gen2" && !gen2NameOK.MatchString(value):
			return "Gen2 names: letters, numbers, space, - and _ only."
		}
	case "led":
		if n, err := strconv.Atoi(value); err != nil || n < 0 || n > 4 {
			return "LED brightness must be 0 to 4."
		}
	case "webauth":
		if value != "off" {
			return "Web password can only be turned off here."
		}
	case "identify":
	case "groupprofile":
		if kind != "gen1" {
			return "Group profiles can only be applied to GigaCore Gen1 for now."
		}
		if _, err := parseGroupProfile(value); err != nil {
			return err.Error()
		}
	case "groupnames":
		if kind != "gen1" {
			return "Group names can only be set on GigaCore Gen1 for now."
		}
		if _, err := parseGroupNames(value); err != nil {
			return err.Error()
		}
	default:
		return "Unknown setting " + action + "."
	}
	return ""
}

func formPost(d luminex.Device, path, body string) request {
	return request{"POST", base(d.APIHost()) + path, "application/x-www-form-urlencoded", body}
}

func getText(d luminex.Device, path string, auth *Auth) (string, error) {
	r, err := deviceFetch("GET", base(d.APIHost())+path, "", "", auth, 6*time.Second)
	if err != nil {
		return "", err
	}
	if r.Status != http.StatusOK {
		return "", fmt.Errorf("device answered HTTP %d", r.Status)
	}
	return strings.TrimSpace(r.Text), nil
}

// settingSteps builds the writes for one device and a read-back that returns what the device now
// reports (and an error text if it differs from what was asked).
func settingSteps(d luminex.Device, action, value string, auth *Auth) ([]request, func() (string, error), error) {
	qe := url.QueryEscape
	if d.APIKind == "luminode" {
		return nodeSteps(d, action, value, auth)
	}
	if d.APIKind == "gen1" {
		save := formPost(d, "/config/icfg_conf_save", "save=1")
		switch action {
		case "name":
			txt, err := getText(d, "/config/switchlegend", auth)
			if err != nil {
				return nil, nil, fmt.Errorf("could not read the current name: %w", err)
			}
			f := strings.Split(txt, ",")
			if len(f) < 3 {
				return nil, nil, fmt.Errorf("unexpected reply when reading the name: %q", txt)
			}
			descr, _ := url.PathUnescape(f[1])
			steps := []request{formPost(d, "/config/switchlegend", "sw_name="+qe(value)+"&sw_id="+qe(f[2])+"&sw_descr="+qe(descr)), save}
			return steps, func() (string, error) {
				t, err := getText(d, "/config/switchlegend", auth)
				if err != nil {
					return "", err
				}
				got, _ := url.PathUnescape(strings.Split(t, ",")[0])
				if got != value {
					return got, fmt.Errorf("it still reports the name %q", got)
				}
				return got, nil
			}, nil
		case "led":
			return []request{formPost(d, "/config/lmx_led", "brightness="+value), save}, func() (string, error) {
				t, err := getText(d, "/config/lmx_led", auth)
				got := strings.TrimSuffix(t, "|")
				if err == nil && got != value {
					err = fmt.Errorf("it still reports brightness %s", got)
				}
				return got, err
			}, nil
		case "identify":
			return []request{formPost(d, "/config/lmx", "wink=1")}, nil, nil
		case "groupnames":
			return groupNameSteps(d, value, auth)
		case "groupprofile":
			return groupProfileSteps(d, value, auth)
		}
		return nil, nil, fmt.Errorf("unknown setting %q", action)
	}

	// Gen2: from the WebApi 1.5.0 spec, not yet tried on a real switch.
	put := func(path, body string) request {
		return request{"PUT", base(d.APIHost()) + path, "application/json", body}
	}
	check := func(path, want string) func() (string, error) {
		return func() (string, error) {
			t, err := getText(d, path, auth)
			if err == nil && t != want {
				err = fmt.Errorf("it reports %s", t)
			}
			return t, err
		}
	}
	switch action {
	case "name":
		rq, _, err := nameRequest(d, auth, value)
		if err != nil {
			return nil, nil, err
		}
		b, _ := json.Marshal(value)
		return []request{rq}, check("/api/device/name", string(b)), nil
	case "led":
		return []request{put("/api/led/brightness", value)}, check("/api/led/brightness", value), nil
	case "identify":
		return []request{put("/api/identify", `{"duration":9}`)}, nil, nil
	case "webauth":
		return []request{put("/api/security/enabled", "false")}, check("/api/security/enabled", "false"), nil
	}
	return nil, nil, fmt.Errorf("unknown setting %q", action)
}

// applySetting sends the writes for one device and reads the result back.
func applySetting(d luminex.Device, action, value string, auth *Auth) (string, string) {
	steps, verify, err := settingSteps(d, action, value, auth)
	if err != nil {
		return "failed", err.Error()
	}
	var dry []string
	dropped := false
	for _, rq := range steps {
		r := sendWrite(d, nil, rq, auth)
		switch r.Outcome {
		case "dry":
			dry = append(dry, r.Request)
		case "auth":
			return "failed", "Asked for a password on the change."
		case "failed":
			return "failed", r.Reason
		case "dropped":
			// GigaCore Gen1 drops the connection after a config POST that worked (CLAUDE.md), so keep going to the
			// save and let the read-back say whether it took. Anything else that drops did not get through.
			if d.APIKind != "gen1" {
				return "failed", "Connection dropped before confirming: " + r.Reason
			}
			dropped = true
		}
	}
	if len(dry) > 0 {
		return "dry_run", "Would send: " + strings.Join(dry, "  then  ")
	}
	gen2 := ""
	if d.APIKind == "gen2" {
		gen2 = " (GigaCore Gen2 calls are from the spec and not yet confirmed on a real switch.)"
	}
	if verify == nil {
		// Nothing to read back (identify), so a dropped connection needs another check: the cable may have just been pulled.
		if dropped && !isUp(d, auth) {
			return "failed", "Connection dropped and it is not answering. Check cable and power."
		}
		return "done", "Sent." + gen2
	}
	got, err := verify()
	if err != nil {
		return "failed", "Sent, but could not confirm: " + err.Error()
	}
	return "done", "Confirmed: it now reports " + strconv.Quote(got) + "." + gen2
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Items  []struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Items) == 0 {
		writeJSON(w, 400, map[string]string{"error": "No devices selected."})
		return
	}
	switch body.Action {
	case "name", "led", "identify", "webauth", "groupnames", "groupprofile":
	default:
		writeJSON(w, 400, map[string]string{"error": "Unknown setting " + body.Action + "."})
		return
	}
	var ids []string
	values := map[string]string{}
	for _, it := range body.Items {
		ids = append(ids, it.ID)
		values[it.ID] = it.Value
	}
	devs, msg := lookup(ids)
	if msg != "" {
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	run := &SettingsRun{id: strconv.FormatInt(nowMs(), 36), action: body.Action}
	for _, d := range devs {
		run.items = append(run.items, &SettingsItem{ID: d.ID, Device: d, Value: values[d.ID], Status: "queued", Label: settingStatusLabel["queued"]})
	}
	state.mu.Lock()
	if (state.job != nil && !state.job.snapshot().Done) || (state.settings != nil && !state.settings.snapshot().Done) {
		state.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "Busy: a job is running."})
		return
	}
	state.settings = run
	state.mu.Unlock()
	notify()
	go run.start()
	writeJSON(w, 202, map[string]any{"ok": true, "runId": run.id})
}

// nodeSteps handles a LumiNode / LumiCore. The web password goes off with PUT /api/deviceinfo/auth and is read back
// from GET /api/deviceinfo/auth. A rename uses the shared nameRequest (PUT /api/deviceinfo, which wants the ID and
// colors sent back) and reads short_name back.
func nodeSteps(d luminex.Device, action, value string, auth *Auth) ([]request, func() (string, error), error) {
	switch action {
	case "webauth":
		rq := request{"PUT", base(d.APIHost()) + "/api/deviceinfo/auth", "application/json", `{"web_auth_enabled":false}`}
		return []request{rq}, func() (string, error) {
			t, err := getText(d, "/api/deviceinfo/auth", auth)
			if err != nil {
				return "", err
			}
			var got struct {
				On *bool `json:"web_auth_enabled"`
			}
			if json.Unmarshal([]byte(t), &got) != nil || got.On == nil {
				return "", fmt.Errorf("unexpected reply when reading the setting: %q", t)
			}
			if *got.On {
				return "password on", fmt.Errorf("it still reports the web password as on")
			}
			return "password off", nil
		}, nil
	case "identify":
		// Spec v2.7.2 / v2.9.1: POST /api/identify with an empty JSON object, 204; the LEDs and display change. No read-back.
		return []request{{"POST", base(d.APIHost()) + "/api/identify", "application/json", "{}"}}, nil, nil
	case "name":
	default:
		return nil, nil, fmt.Errorf("only the name, the web password and Identify can be used on a LumiNode from this page")
	}
	rq, _, err := nameRequest(d, auth, value)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the current device info: %w", err)
	}
	return []request{rq}, func() (string, error) {
		t, err := getText(d, "/api/deviceinfo", auth)
		if err != nil {
			return "", err
		}
		var got struct {
			Short string `json:"short_name"`
		}
		_ = json.Unmarshal([]byte(t), &got)
		if got.Short != value {
			return got.Short, fmt.Errorf("it still reports the name %q", got.Short)
		}
		return got.Short, nil
	}, nil
}

// settingsBusy says whether a running settings run still has a device matching match to do.
func settingsBusy(match func(*SettingsItem) bool) bool {
	state.mu.Lock()
	run := state.settings
	state.mu.Unlock()
	if run == nil {
		return false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	for _, it := range run.items {
		switch it.Status {
		case "queued", "checking", "sending":
			if match(it) {
				return true
			}
		}
	}
	return false
}

// settingsRunning says whether a settings run is still sending.
func settingsRunning() bool { return settingsBusy(func(*SettingsItem) bool { return true }) }
