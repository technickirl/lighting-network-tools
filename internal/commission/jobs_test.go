package commission

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// A real device (no serial in TXT) identified by its instance name.
func realDevice(ip string) luminex.Device {
	return luminex.Device{ID: "LUM546436890", Instance: "LUM546436890", IP: ip, APIKind: "gen2", Family: "gigacore", Model: "GigaCore16tf"}
}

func waitDone(t *testing.T, j *Job) *JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := j.snapshot(); s.Done {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job never finished: %+v", j.snapshot().Items)
	return nil
}

func TestFindSameByInstanceName(t *testing.T) {
	seen := []luminex.Device{realDevice("192.168.4.3")}
	if s := findSame(seen, realDevice("2.0.0.1")); s == nil || s.IP != "192.168.4.3" {
		t.Fatalf("device not recognised after its IP changed: %+v", s)
	}
}

func TestApplyConfirmedAtTarget(t *testing.T) {
	it := &Item{ID: "LUM546436890", Device: realDevice("2.0.0.1"), TargetIP: "192.168.4.3"}
	j := newJob("apply", []*Item{it}, func() {}, nil)
	j.set(it, "rebooting", "")
	j.mu.Lock()
	it.SentAt = nowMs()
	j.mu.Unlock()
	j.onScan([]luminex.Device{realDevice("192.168.4.3")})
	if s := j.snapshot(); s.Items[0].Status != "done" || s.Items[0].NewIP != "192.168.4.3" {
		t.Errorf("expected done at target: %+v", s.Items[0])
	}
}

// A mask-only change starts out at its target address, so that alone must not confirm it.
func TestMaskChangeWaitsForTheMask(t *testing.T) {
	it := &Item{ID: "LUM546436890", Device: realDevice("192.168.4.3"), TargetIP: "192.168.4.3"}
	j := newJob("apply", []*Item{it}, func() {}, nil)
	j.Netmask = "255.255.255.0"
	j.set(it, "rebooting", "")
	j.mu.Lock()
	it.SentAt = nowMs()
	j.mu.Unlock()
	seen := realDevice("192.168.4.3")
	seen.Prefix = 16
	j.onScan([]luminex.Device{seen})
	if s := j.snapshot(); s.Items[0].Status != "rebooting" {
		t.Errorf("still on the old mask, so not confirmed yet: %+v", s.Items[0])
	}
	seen.Prefix = 24
	j.onScan([]luminex.Device{seen})
	if s := j.snapshot(); s.Items[0].Status != "done" {
		t.Errorf("expected done once the mask is right: %+v", s.Items[0])
	}
}

func TestCancelWhileRebootingFinishesJob(t *testing.T) {
	it := &Item{ID: "LUM546436890", Device: realDevice("2.0.0.1"), TargetIP: "192.168.4.3"}
	j := newJob("apply", []*Item{it}, func() {}, nil)
	j.set(it, "rebooting", "")
	j.mu.Lock()
	it.SentAt = nowMs()
	j.mu.Unlock()
	j.cancel()
	s := waitDone(t, j)
	if s.Items[0].Status != "not_seen" {
		t.Errorf("rebooting item after cancel: %q", s.Items[0].Status)
	}
}

func TestCancelThenRebootingStillFinishes(t *testing.T) {
	it := &Item{ID: "LUM546436890", Device: realDevice("2.0.0.1"), TargetIP: "192.168.4.3"}
	j := newJob("apply", []*Item{it}, func() {}, nil)
	j.set(it, "sending", "")
	j.cancel()
	j.set(it, "rebooting", "")
	j.mu.Lock()
	it.SentAt = nowMs()
	j.mu.Unlock()
	j.ensureVerifier()
	s := waitDone(t, j)
	if s.Items[0].Status != "not_seen" {
		t.Errorf("status %q", s.Items[0].Status)
	}
}

// stubEvidence replaces the verifier's HTTP probe and mDNS scan for one test.
func stubEvidence(t *testing.T, up bool, scan func() ([]luminex.Device, error)) {
	t.Helper()
	oldProbe, oldScan := probeUp, scanFor
	probeUp = func(luminex.Device, *Auth) bool { return up }
	scanFor = func(time.Duration) ([]luminex.Device, error) { return scan() }
	t.Cleanup(func() { probeUp, scanFor = oldProbe, oldScan })
}

// rebootingJob returns a job of the given kind whose one device (at 2.0.0.1,
// target 192.168.4.3) was sent its write 20 s ago, so the "settled" 10 s rule
// in onScan is already satisfied.
func rebootingJob(kind string) (*Job, *Item) {
	it := &Item{ID: "LUM546436890", Device: realDevice("2.0.0.1"), TargetIP: "192.168.4.3"}
	j := newJob(kind, []*Item{it}, func() {}, &sync.Mutex{})
	j.set(it, "rebooting", "")
	j.mu.Lock()
	it.SentAt = time.Now().Add(-20 * time.Second).UnixMilli()
	j.mu.Unlock()
	return j, it
}

func TestBusyDeviceStillInMdnsIsNotDown(t *testing.T) {
	// The device misses one HTTP probe while applying the new address but
	// still advertises the old one: that is not "went down", so the sighting
	// at the old address must not become "Came back at <old> instead of <target>".
	j, it := rebootingJob("apply")
	stubEvidence(t, false, func() ([]luminex.Device, error) { return []luminex.Device{realDevice("2.0.0.1")}, nil })
	j.verifyPass()
	j.mu.Lock()
	down := it.wentDown
	j.mu.Unlock()
	if down {
		t.Error("device counted as down while mDNS still saw it")
	}
	if s := j.snapshot(); s.Items[0].Status != "rebooting" {
		t.Errorf("status %q (%s), wanted still rebooting", s.Items[0].Status, s.Items[0].Detail)
	}
}

func TestResetConfirmedAfterLeavingMdns(t *testing.T) {
	// A reset of a device already on its factory address: it leaves mDNS and
	// stops answering, then comes back at the same address.
	j, it := rebootingJob("reset")
	stubEvidence(t, false, func() ([]luminex.Device, error) { return nil, nil })
	j.verifyPass()
	j.mu.Lock()
	down := it.wentDown
	j.mu.Unlock()
	if !down {
		t.Fatal("device not counted as down when both probe and mDNS missed it")
	}
	stubEvidence(t, true, func() ([]luminex.Device, error) { return []luminex.Device{realDevice("2.0.0.1")}, nil })
	j.verifyPass()
	if s := j.snapshot(); s.Items[0].Status != "done" {
		t.Errorf("status %q, wanted done", s.Items[0].Status)
	}
}

func TestApplySeenAtTargetDuringPass(t *testing.T) {
	// The "changed IP" path is untouched: seen at the target, done at once.
	j, _ := rebootingJob("apply")
	stubEvidence(t, false, func() ([]luminex.Device, error) { return []luminex.Device{realDevice("192.168.4.3")}, nil })
	j.verifyPass()
	if s := j.snapshot(); s.Items[0].Status != "done" || s.Items[0].NewIP != "192.168.4.3" {
		t.Errorf("expected done at target: %+v", s.Items[0])
	}
}

func TestResetOnAnAddressOutside2xIsConfirmed(t *testing.T) {
	// Not every device defaults to 2.x, so a reset is confirmed by the device coming back, wherever that is.
	j, _ := rebootingJob("reset")
	stubEvidence(t, false, func() ([]luminex.Device, error) { return []luminex.Device{realDevice("192.168.1.99")}, nil })
	j.verifyPass()
	s := j.snapshot().Items[0]
	if s.Status != "done" || s.NewIP != "192.168.1.99" || strings.Contains(s.Detail, "factory default") {
		t.Errorf("expected a confirmed reset with no factory default claim: %+v", s)
	}
}

func TestScanErrorIsNotEvidenceOfDown(t *testing.T) {
	j, it := rebootingJob("apply")
	stubEvidence(t, false, func() ([]luminex.Device, error) { return nil, errors.New("cannot bind UDP 5353") })
	j.verifyPass()
	j.mu.Lock()
	down := it.wentDown
	j.mu.Unlock()
	if down {
		t.Error("a failed scan counted as the device leaving mDNS")
	}
	if s := j.snapshot(); s.Items[0].Status != "rebooting" {
		t.Errorf("status %q, wanted still rebooting", s.Items[0].Status)
	}
}

func TestDoScanReportsError(t *testing.T) {
	stubEvidence(t, true, func() ([]luminex.Device, error) { return nil, errors.New("cannot bind UDP 5353: in use") })
	state.mu.Lock()
	state.devices, state.scanError, state.scanning = []luminex.Device{realDevice("2.0.0.1")}, "", false
	state.mu.Unlock()
	doScan()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.scanError != "cannot bind UDP 5353: in use" {
		t.Errorf("scanError %q", state.scanError)
	}
	if state.scanning {
		t.Error("still marked scanning after a failed scan")
	}
	if len(state.devices) != 1 {
		t.Errorf("a failed scan wiped the device list: %+v", state.devices)
	}
}

func TestSnapshotReportsLostLink(t *testing.T) {
	en0 := luminex.Iface{Name: "en0", Address: "100.105.182.186", Prefix: 21}
	lan := luminex.Iface{Name: "en11", Address: "192.168.100.56", Prefix: 16}
	old := localIfaces
	t.Cleanup(func() { localIfaces = old })
	state.mu.Lock()
	state.goodIfaces = []luminex.Iface{en0, lan} // what the machine had when devices were last reachable
	state.mu.Unlock()
	localIfaces = func() []luminex.Iface { return []luminex.Iface{en0} } // cable out: en11 lost its address
	var s Snapshot
	if err := json.Unmarshal(snapshot(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.LinkLost) != 1 || s.LinkLost[0].Name != "en11" || s.LinkLost[0].Address != "192.168.100.56" {
		t.Fatalf("linkLost = %+v, wanted en11 192.168.100.56", s.LinkLost)
	}
	localIfaces = func() []luminex.Iface { return []luminex.Iface{en0, lan} } // link back
	if err := json.Unmarshal(snapshot(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.LinkLost) != 0 {
		t.Fatalf("linkLost = %+v after the link came back", s.LinkLost)
	}
}

// Cancel pressed while a device is still being checked: the check can take
// seconds, and the reset must not go out once it answers.
func TestCancelDuringCheckSendsNothing(t *testing.T) {
	asked, release := make(chan struct{}, 1), make(chan struct{})
	f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			select {
			case asked <- struct{}{}:
			default:
			}
			<-release
		}
		w.WriteHeader(200)
	})
	it := &Item{ID: "LUM546436890", Device: luminex.Device{ID: "LUM546436890", IP: "127.0.0.1", LinkRoute: f.addr(), APIKind: "gen2", Family: "gigacore"}}
	j := newJob("reset", []*Item{it}, func() {}, &sync.Mutex{})
	done := make(chan struct{})
	go func() { j.runOne(it, nil); close(done) }()
	select {
	case <-asked:
	case <-time.After(2 * time.Second):
		t.Fatal("the device was never checked")
	}
	j.cancel()
	close(release)
	<-done
	for _, r := range f.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("sent after Cancel: %s %s", r.Method, r.Path)
		}
	}
	if s := waitDone(t, j); s.Items[0].Status != "failed" || !strings.Contains(s.Items[0].Detail, "Cancelled before sending") {
		t.Errorf("item after cancel: %+v", s.Items[0])
	}
}

// A reset whose connection was lost after sending proves nothing by itself: a device that keeps answering and
// stays in mDNS at its address did not restart, however long ago the request went.
func TestResetIsNotConfirmedByADroppedConnectionAlone(t *testing.T) {
	j, it := rebootingJob("reset")
	stubEvidence(t, true, func() ([]luminex.Device, error) { return []luminex.Device{realDevice("2.0.0.1")}, nil })
	j.verifyPass()
	j.mu.Lock()
	down := it.wentDown
	j.mu.Unlock()
	if s := j.snapshot(); down || s.Items[0].Status != "rebooting" {
		t.Errorf("down=%v status %q (%s), wanted still waiting", down, s.Items[0].Status, s.Items[0].Detail)
	}
}

func TestDroppedWriteDoesNotCountAsWentDown(t *testing.T) {
	f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			return
		}
		if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
			c.Close()
		}
	})
	stubEvidence(t, true, func() ([]luminex.Device, error) { return nil, errors.New("no scan in this test") })
	oldReach := reachability
	reachability = func(string, int) luminex.Reach { return luminex.Reach{Reachable: true, Via: "test"} }
	t.Cleanup(func() { reachability = oldReach })
	it := &Item{ID: "x", Device: luminex.Device{ID: "x", IP: f.addr(), APIKind: "luminode"}}
	j := newJob("reset", []*Item{it}, func() {}, &sync.Mutex{})
	j.runOne(it, nil)
	j.mu.Lock()
	down, status := it.wentDown, it.Status
	j.mu.Unlock()
	j.cancel()
	if down || status != "rebooting" {
		t.Errorf("down=%v status=%q, wanted rebooting and not yet down", down, status)
	}
}

// Two password posts close together must not both run the same item: its write would go out twice.
func TestPasswordRetryTakesEachItemOnce(t *testing.T) {
	var mu sync.Mutex
	writes := 0
	f := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mu.Lock()
			writes++
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
		w.WriteHeader(204)
	})
	stubEvidence(t, true, func() ([]luminex.Device, error) { return nil, errors.New("no scan in this test") })
	oldReach := reachability
	reachability = func(string, int) luminex.Reach { return luminex.Reach{Reachable: true, Via: "test"} }
	t.Cleanup(func() { reachability = oldReach })
	it := &Item{ID: "x", Device: luminex.Device{ID: "x", IP: f.addr(), APIKind: "luminode"}}
	j := newJob("reset", []*Item{it}, func() {}, &sync.Mutex{})
	j.set(it, "needs_password", "")
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); j.retryWithPassword([]string{"x"}, &Auth{Username: "admin", Password: "pw"}) }()
	}
	wg.Wait()
	j.cancel()
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 {
		t.Errorf("the reset went out %d times", writes)
	}
}

func TestPasswordRetryLeavesACancelledJobFinished(t *testing.T) {
	it := &Item{ID: "x", Device: realDevice("2.0.0.1")}
	j := newJob("reset", []*Item{it}, func() {}, &sync.Mutex{})
	j.set(it, "needs_password", "")
	j.cancel()
	j.mu.Lock()
	it.Status = "needs_password" // a write answered 401 after the cancel
	j.Done = true
	j.mu.Unlock()
	j.retryWithPassword([]string{"x"}, &Auth{Username: "admin"})
	if s := j.snapshot(); !s.Done {
		t.Error("a retry reopened a cancelled job, which nothing would ever finish")
	}
}
