package commission

// A job is one batch of writes: either "apply" (set each device's IP) or
// "reset" (factory reset). Each device goes
//   queued → checking → (needs password) → sending → rebooting → done | not seen | failed
// and success is confirmed by watching mDNS for the same serial to come back:
// for apply, at the requested address; for reset, at a new/default address.

import (
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	verifyScan  = 4 * time.Second
	verifyGap   = 4 * time.Second
	concurrency = 4
)

var statusLabel = map[string]string{
	"queued":         "Queued",
	"checking":       "Checking…",
	"needs_password": "Needs password",
	"sending":        "Sending…",
	"rebooting":      "Rebooting…",
	"done":           "Done ✓",
	"not_seen":       "Not seen afterwards",
	"failed":         "Failed",
	"dry_run":        "Dry run (not sent)",
}

type Item struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Label    string         `json:"label"`
	Detail   string         `json:"detail"`
	TargetIP string         `json:"targetIp"` // apply: the address we asked for
	NewIP    string         `json:"newIp"`    // where it was seen afterwards
	SentAt   int64          `json:"sentAt"`
	Device   luminex.Device `json:"device"`

	auth     *Auth
	wentDown bool
}

type Counts struct {
	Total         int `json:"total"`
	OK            int `json:"ok"`
	Failed        int `json:"failed"`
	Pending       int `json:"pending"`
	NeedsPassword int `json:"needsPassword"`
	Dry           int `json:"dry"`
}

type JobSnapshot struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	KeepProfiles bool   `json:"keepProfiles"`
	KeepIP       bool   `json:"keepIp"`
	StartedAt    int64  `json:"startedAt"`
	Done         bool   `json:"done"`
	DryRun       bool   `json:"dryRun"`
	Counts       Counts `json:"counts"`
	Items        []Item `json:"items"`
}

type Job struct {
	mu           sync.Mutex
	ID           string
	Kind         string // apply | reset
	KeepProfiles bool
	KeepIP       bool
	Netmask      string
	Gateway      string
	StartedAt    time.Time
	Items        []*Item
	Done         bool
	cancelled    bool
	verifying    bool
	notify       func()
	seen         func([]luminex.Device) // verify-scan results, so the device list follows the new addresses
	scanLock     *sync.Mutex
}

func nowMs() int64 { return time.Now().UnixMilli() }

func newJob(kind string, items []*Item, notify func(), scanLock *sync.Mutex) *Job {
	j := &Job{ID: strconv.FormatInt(nowMs(), 36), Kind: kind, StartedAt: time.Now(), notify: notify, scanLock: scanLock, Items: items}
	for _, it := range items {
		it.Status, it.Label = "queued", statusLabel["queued"]
	}
	return j
}

func (j *Job) verifyTimeout() time.Duration {
	if j.Kind == "reset" {
		return 3 * time.Minute
	}
	return 2 * time.Minute
}

func (j *Job) doneLabel() string {
	if j.Kind == "reset" {
		return "Reset ✓"
	}
	return "Addressed ✓"
}

func (j *Job) snapshot() *JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := &JobSnapshot{ID: j.ID, Kind: j.Kind, KeepProfiles: j.KeepProfiles, KeepIP: j.KeepIP, StartedAt: j.StartedAt.UnixMilli(),
		Done: j.Done, DryRun: luminex.DryRun, Counts: j.countsLocked()}
	for _, it := range j.Items {
		s.Items = append(s.Items, *it)
	}
	return s
}

func (j *Job) countsLocked() Counts {
	c := Counts{Total: len(j.Items)}
	for _, i := range j.Items {
		switch i.Status {
		case "done":
			c.OK++
		case "failed", "not_seen":
			c.Failed++
		case "needs_password":
			c.NeedsPassword++
		case "dry_run":
			c.Dry++
		default:
			c.Pending++
		}
	}
	return c
}

func (j *Job) set(it *Item, status, detail string) {
	label := statusLabel[status]
	if status == "done" {
		label = j.doneLabel()
	}
	j.mu.Lock()
	it.Status, it.Label, it.Detail = status, label, detail
	j.mu.Unlock()
	id := it.ID
	if detail != "" {
		luminex.Logf("[%s] %s — %s", id, label, detail)
	} else {
		luminex.Logf("[%s] %s", id, label)
	}
	j.notify()
}

func (j *Job) start() {
	mode := ""
	if luminex.DryRun {
		mode = " (DRY RUN)"
	}
	luminex.Logf("%s job %s: %d device(s)%s", strings.ToUpper(j.Kind[:1])+j.Kind[1:], j.ID, len(j.Items), mode)
	j.runMany(j.Items, nil)
	j.checkDone()
}

func (j *Job) runMany(items []*Item, auth *Auth) {
	queue := make(chan *Item, len(items))
	for _, it := range items {
		queue <- it
	}
	close(queue)
	var wg sync.WaitGroup
	for w := 0; w < min(concurrency, len(items)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range queue {
				if j.isCancelled() {
					return
				}
				j.runOne(it, auth)
			}
		}()
	}
	wg.Wait()
}

func (j *Job) isCancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelled
}

func (j *Job) runOne(it *Item, userAuth *Auth) {
	d := it.Device
	if d.APIKind == "" {
		j.set(it, "failed", fmt.Sprintf("Unknown device type %q.", d.Model))
		return
	}
	if d.IP == "" {
		j.set(it, "failed", "No IP address reported.")
		return
	}
	if r := reachability(d.IP, d.Prefix); !r.Reachable && d.LinkRoute == "" {
		j.set(it, "failed", "Unreachable. "+r.Reason+" "+r.Fix)
		return
	}

	if userAuth != nil {
		j.set(it, "checking", "Trying the password…")
	} else {
		j.set(it, "checking", "Checking the device answers…")
	}
	auth, needsPw, err := findAuth(d, userAuth)
	// Cancel pressed while this device was being checked (the check can take
	// several seconds): nothing has been sent yet, and nothing is.
	if j.isCancelled() {
		j.set(it, "failed", "Cancelled before sending.")
		return
	}
	if err != nil {
		j.set(it, "failed", fmt.Sprintf("No answer from %s (%s). Check cable and power.", d.IP, err))
		return
	}
	if needsPw {
		if userAuth != nil {
			j.set(it, "needs_password", "Password rejected. Try again.")
		} else {
			j.set(it, "needs_password", "Password needed.")
		}
		return
	}
	j.mu.Lock()
	it.auth = auth
	j.mu.Unlock()
	luminex.RememberAuth(d.IP, auth)

	// Names, LEDs and the web password are a settings run (settings.go) that the
	// page sends after the addresses are confirmed, so this job only addresses.
	var pre *request
	var rq request
	if j.Kind == "apply" {
		pre, rq = ipRequests(d, IPPlan{IP: it.TargetIP, Netmask: j.Netmask, Gateway: j.Gateway})
		j.set(it, "sending", "Setting "+it.TargetIP+"…")
	} else {
		rq = resetRequest(d, j.KeepProfiles, j.KeepIP)
		j.set(it, "sending", "")
	}
	r := sendWrite(d, pre, rq, auth)
	switch r.Outcome {
	case "dry":
		j.set(it, "dry_run", "Would send: "+r.Request)
		return
	case "auth":
		j.set(it, "needs_password", "Asked for a password on the change.")
		return
	case "failed":
		j.set(it, "failed", r.Reason)
		return
	case "dropped":
		j.mu.Lock()
		it.SentAt, it.wentDown = nowMs(), true
		j.mu.Unlock()
		j.set(it, "rebooting", fmt.Sprintf("Connection dropped after sending (%s), probably accepted. Waiting for it…", r.Reason))
	case "sent":
		j.mu.Lock()
		it.SentAt = nowMs()
		j.mu.Unlock()
		j.set(it, "rebooting", fmt.Sprintf("Accepted (HTTP %d). Waiting for it…", r.Status))
	}
	j.ensureVerifier()
}

func (j *Job) retryWithPassword(ids []string, auth *Auth) {
	j.mu.Lock()
	j.Done = false
	var items []*Item
	for _, it := range j.Items {
		if it.Status == "needs_password" && contains(ids, it.ID) {
			items = append(items, it)
		}
	}
	j.mu.Unlock()
	for _, it := range items {
		j.set(it, "queued", "")
	}
	j.runMany(items, auth)
	j.checkDone()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ── Verification ──────────────────────────────────────────────────────────

// The verifier's two sources of evidence, as variables so tests can stand in
// for a device and the network.
var (
	probeUp = isUp
	scanFor = luminex.Scan
)

func (j *Job) ensureVerifier() {
	j.mu.Lock()
	if j.verifying {
		j.mu.Unlock()
		return
	}
	j.verifying = true
	j.mu.Unlock()
	go func() {
		j.verifyLoop()
		j.mu.Lock()
		j.verifying = false
		j.mu.Unlock()
		// A cancel stops the loop early; nothing will resolve what is still
		// rebooting, so say so rather than leave the job running for ever.
		if j.isCancelled() {
			j.abandonRebooting()
		}
		j.checkDone()
	}()
}

// abandonRebooting gives every "rebooting" item a final status after a cancel.
// It runs from cancel() and again when the verifier exits, which covers a
// device whose write was already in flight when the cancel arrived.
func (j *Job) abandonRebooting() {
	what := "change"
	if j.Kind == "reset" {
		what = "reset"
	}
	for _, it := range j.rebooting() {
		j.set(it, "not_seen", "Cancelled while waiting. The "+what+" was sent and may still happen; Scan to find it.")
	}
}

func (j *Job) rebooting() []*Item {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*Item
	for _, it := range j.Items {
		if it.Status == "rebooting" {
			out = append(out, it)
		}
	}
	return out
}

func (j *Job) verifyLoop() {
	for !j.isCancelled() && len(j.rebooting()) > 0 {
		j.verifyPass()
		if len(j.rebooting()) == 0 {
			break
		}
		time.Sleep(verifyGap)
	}
}

// verifyPass gathers one round of evidence: an HTTP probe of each old address
// and one mDNS scan, run side by side so they describe the same moment.
//
// A device counts as "went down" only when its probe failed AND the scan did
// not see it. One failed 2.5 s probe on its own is not enough: a device still
// busy applying the change can miss a probe while it still advertises its old
// address, and that single miss used to make the next sighting at the old
// address read as "Came back at <old> instead of <target>" (or, for a reset,
// "Reset ✓") while the device was mid-change.
func (j *Job) verifyPass() {
	var wg sync.WaitGroup
	var pmu sync.Mutex
	noAnswer := map[*Item]bool{}
	for _, it := range j.rebooting() {
		j.mu.Lock()
		down, auth := it.wentDown, it.auth
		j.mu.Unlock()
		if down {
			continue
		}
		wg.Add(1)
		go func(it *Item, auth *Auth) {
			defer wg.Done()
			if !probeUp(it.Device, auth) {
				pmu.Lock()
				noAnswer[it] = true
				pmu.Unlock()
			}
		}(it, auth)
	}

	j.scanLock.Lock()
	seen, scanErr := scanFor(verifyScan)
	j.scanLock.Unlock()
	wg.Wait()

	if scanErr != nil {
		// No mDNS evidence this pass: neither "went down" nor "came back"
		// can be judged, only the timeout below.
		luminex.Warnf("verify scan failed: %v", scanErr)
	} else {
		for it := range noAnswer {
			if s := findSame(seen, it.Device); s != nil {
				luminex.Logf("[%s] not answering HTTP on %s but still in mDNS at %s — still applying, not down yet", it.ID, it.Device.IP, s.IP)
				continue
			}
			j.mu.Lock()
			it.wentDown = true
			j.mu.Unlock()
			luminex.Logf("[%s] stopped answering on %s and left mDNS — rebooting", it.ID, it.Device.IP)
		}
		if j.seen != nil {
			j.seen(seen)
		}
		j.onScan(seen)
	}

	for _, it := range j.rebooting() {
		j.mu.Lock()
		sentAt := it.SentAt
		j.mu.Unlock()
		if time.Since(time.UnixMilli(sentAt)) > j.verifyTimeout() {
			mins := int(j.verifyTimeout().Minutes())
			if s := findSame(seen, it.Device); s != nil && j.Kind == "apply" && s.IP == it.TargetIP {
				j.set(it, "not_seen", fmt.Sprintf("At %s but still /%d, not /%d, after %d min. Power-cycle and Scan.", s.IP, s.Prefix, maskToCIDR(j.Netmask), mins))
			} else if s != nil {
				j.set(it, "not_seen", fmt.Sprintf("Still on old address %s after %d min. Power-cycle and Scan.", s.IP, mins))
			} else {
				j.set(it, "not_seen", fmt.Sprintf("Not seen after %d min. Power-cycle and Scan.", mins))
			}
		}
	}
}

// onScan is also called for user-triggered rescans.
func (j *Job) onScan(seen []luminex.Device) {
	for _, it := range j.rebooting() {
		s := findSame(seen, it.Device)
		if s == nil || s.IP == "" {
			continue
		}
		j.mu.Lock()
		down, sentAt, target := it.wentDown, it.SentAt, it.TargetIP
		j.mu.Unlock()
		changed := s.IP != it.Device.IP
		settled := down && time.Since(time.UnixMilli(sentAt)) > 10*time.Second
		switch j.Kind {
		case "apply":
			if s.IP == target {
				// A mask-only change starts out at its target, so the address alone proves nothing:
				// wait until the device reports the mask too, or the timeout says it did not take.
				if j.Netmask != "" && s.Prefix != 0 && s.Prefix != maskToCIDR(j.Netmask) {
					continue
				}
				j.mu.Lock()
				it.NewIP = s.IP
				auth := it.auth
				j.mu.Unlock()
				luminex.RememberAuth(s.IP, auth)
				j.set(it, "done", "Now at "+s.IP+".")
			} else if changed || settled {
				j.mu.Lock()
				it.NewIP = s.IP
				j.mu.Unlock()
				j.set(it, "failed", fmt.Sprintf("Came back at %s, not %s.", s.IP, target))
			}
		default:
			if changed || settled {
				j.mu.Lock()
				it.NewIP = s.IP
				j.mu.Unlock()
				// Many devices come back on 2.x.x.x/8 after a reset, but not all
				// (Nick, 6 Nov 2026), so the address is reported without a label.
				msg := "Back on the network at " + s.IP
				if s.Name != "" {
					msg += fmt.Sprintf(" as %q", s.Name)
				}
				j.set(it, "done", msg+".")
			}
		}
	}
}

// findSame finds d in a later scan, whatever address it has now.
func findSame(list []luminex.Device, d luminex.Device) *luminex.Device {
	for i := range list {
		if luminex.SameDevice(list[i], d) {
			return &list[i]
		}
	}
	return nil
}

func (j *Job) checkDone() {
	j.mu.Lock()
	if j.Done {
		j.mu.Unlock()
		return
	}
	for _, it := range j.Items {
		switch it.Status {
		case "queued", "checking", "sending", "rebooting":
			j.mu.Unlock()
			return
		}
	}
	j.Done = true
	c := j.countsLocked()
	j.mu.Unlock()
	luminex.Logf("%s job %s finished: %d ok, %d failed, %d waiting for password, %d dry-run", j.Kind, j.ID, c.OK, c.Failed, c.NeedsPassword, c.Dry)
	j.notify()
}

func (j *Job) cancel() {
	j.mu.Lock()
	j.cancelled = true
	var pending []*Item
	for _, it := range j.Items {
		switch it.Status {
		case "queued", "checking", "needs_password":
			pending = append(pending, it)
		}
	}
	j.mu.Unlock()
	for _, it := range pending {
		j.set(it, "failed", "Cancelled before sending.")
	}
	j.abandonRebooting()
	j.checkDone()
}
