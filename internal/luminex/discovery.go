package luminex

// mDNS discovery of _luminex._tcp devices.
//
// Two backends:
//   mdns    native multicast socket. Go's ListenMulticastUDP sets SO_REUSEADDR
//           (and SO_REUSEPORT on macOS/BSD), so it can share UDP 5353 with
//           mDNSResponder on macOS and with Windows' own mDNS. The group is
//           joined on every IPv4 interface and queries are sent out of each.
//   dns-sd  Apple's CLI as child processes (macOS, or Windows with Bonjour).
//           Used when the native socket cannot bind, or MDNS_BACKEND=dns-sd.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

const Service = "_luminex._tcp"

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

type rawService struct {
	Instance string
	Host     string
	Port     int
	Txt      map[string]string
	RawTxt   string
}

var (
	backendMu   sync.Mutex
	backendName string
	backendNote string
)

func haveDnsSd() bool {
	_, err := exec.LookPath("dns-sd")
	return err == nil
}

func chooseBackend() string {
	backendMu.Lock()
	defer backendMu.Unlock()
	if backendName != "" {
		return backendName
	}
	switch os.Getenv("MDNS_BACKEND") {
	case "dns-sd":
		backendName, backendNote = "dns-sd", "forced by MDNS_BACKEND=dns-sd"
	case "mdns":
		backendName, backendNote = "mdns", "forced by MDNS_BACKEND=mdns"
	default:
		backendName, backendNote = "mdns", "native multicast socket on UDP 5353 (shared with the OS mDNS service)"
	}
	Logf("Discovery backend: %s (%s)", backendName, backendNote)
	return backendName
}

func setBackend(name, note string) {
	backendMu.Lock()
	backendName, backendNote = name, note
	backendMu.Unlock()
	Logf("Discovery backend: %s (%s)", name, note)
}

type BackendInfo struct {
	Backend  string `json:"backend"`
	Note     string `json:"note"`
	Platform string `json:"platform"`
	Hostname string `json:"hostname"`
}

func DiscoveryInfo() BackendInfo {
	b := chooseBackend()
	backendMu.Lock()
	note := backendNote
	backendMu.Unlock()
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32" // what the UI checks for
	}
	host, _ := os.Hostname()
	return BackendInfo{Backend: b, Note: note, Platform: platform, Hostname: host}
}

// ───────────────────────── native mDNS backend ─────────────────────────

func buildQuery() ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	name, err := dnsmessage.NewName(Service + ".local.")
	if err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func unescapeName(s string) string {
	s = strings.TrimSuffix(s, ".")
	s = strings.ReplaceAll(s, `\.`, ".")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

func mdnsScan(d time.Duration) ([]rawService, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, mdnsGroup)
	if err != nil {
		return nil, fmt.Errorf("cannot bind UDP 5353: %w", err)
	}
	defer conn.Close()
	pc := ipv4.NewPacketConn(conn)
	_ = pc.SetMulticastLoopback(true)

	// Join the group on every usable interface so queries and answers flow
	// through the NIC that is actually plugged into the switch.
	seenIf := map[int]bool{}
	var ifis []net.Interface
	for _, i := range LocalInterfaces() {
		if seenIf[i.ifi.Index] || i.ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		seenIf[i.ifi.Index] = true
		ifi := i.ifi
		// ListenMulticastUDP has already joined the group on the OS default
		// interface, so that join fails with "address already in use" (worded
		// differently on Windows, where it is WSAEADDRINUSE). The interface is
		// kept for sending whatever the join said: dropping it on the error text
		// used to leave the real NIC out on a Windows PC with a second
		// multicast-capable adapter (VPN, Hyper-V), and the scan found nothing.
		if err := pc.JoinGroup(&ifi, mdnsGroup); err != nil && !strings.Contains(err.Error(), "already in use") {
			Warnf("mdns: join group on %s: %v (still sending on it)", ifi.Name, err)
		}
		ifis = append(ifis, ifi)
	}

	query, err := buildQuery()
	if err != nil {
		return nil, err
	}
	send := func() {
		if len(ifis) == 0 {
			_, _ = pc.WriteTo(query, nil, mdnsGroup)
			return
		}
		for i := range ifis {
			_ = pc.SetMulticastInterface(&ifis[i])
			if _, err := pc.WriteTo(query, nil, mdnsGroup); err != nil {
				Warnf("mdns: send on %s: %v", ifis[i].Name, err)
			}
		}
	}

	found := map[string]*rawService{}
	var mu sync.Mutex
	deadline := time.Now().Add(d)
	_ = conn.SetReadDeadline(deadline)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A malformed packet from anything on the LAN must not take the tool
		// down mid-show (golang.org/x/net before v0.56 could panic on a bad
		// SVCB record), so a parser panic ends this scan, not the process.
		defer func() {
			if r := recover(); r != nil {
				Errorf("mdns: packet parser panic: %v", r)
			}
		}()
		buf := make([]byte, 9000)
		for {
			n, _, _, err := pc.ReadFrom(buf)
			if err != nil {
				return // deadline or closed
			}
			parseMdnsPacket(buf[:n], found, &mu)
		}
	}()

	send()
	requeryUntil(done, deadline, 1500*time.Millisecond, send)
	<-done

	mu.Lock()
	defer mu.Unlock()
	var out []rawService
	for _, s := range found {
		if s.Txt != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

// requeryUntil re-sends the query every interval until the deadline passes or
// the reader goroutine closes done (read error, socket closed). Returning on
// done matters: a closed channel is always ready, so a loop that kept
// selecting on it would spin at full CPU until the deadline.
func requeryUntil(done <-chan struct{}, deadline time.Time, interval time.Duration, send func()) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-tick.C:
			send()
		case <-done:
			return
		case <-timer.C:
			return
		}
	}
}

func parseMdnsPacket(pkt []byte, found map[string]*rawService, mu *sync.Mutex) {
	var p dnsmessage.Parser
	if _, err := p.Start(pkt); err != nil {
		return
	}
	_ = p.SkipAllQuestions()
	var rrs []dnsmessage.Resource
	if a, err := p.AllAnswers(); err == nil {
		rrs = append(rrs, a...)
	}
	if a, err := p.AllAuthorities(); err == nil {
		rrs = append(rrs, a...)
	}
	if a, err := p.AllAdditionals(); err == nil {
		rrs = append(rrs, a...)
	}
	suffix := "." + Service + ".local."
	for _, rr := range rrs {
		name := rr.Header.Name.String()
		if !strings.HasSuffix(strings.ToLower(name), suffix) {
			continue
		}
		instance := unescapeName(name[:len(name)-len(suffix)])
		mu.Lock()
		s := found[instance]
		if s == nil {
			s = &rawService{Instance: instance}
			found[instance] = s
		}
		switch body := rr.Body.(type) {
		case *dnsmessage.TXTResource:
			kv := parseRawTxt(body.TXT)
			if s.Txt == nil {
				Logf("mdns: %q %s", instance, strings.Join(body.TXT, " "))
			}
			s.Txt = kv
			s.RawTxt = strings.Join(body.TXT, " ")
		case *dnsmessage.SRVResource:
			s.Host = unescapeName(body.Target.String())
			s.Port = int(body.Port)
		}
		mu.Unlock()
	}
}

// ───────────────────────── dns-sd backend ─────────────────────────

var dnsSdAddRe = regexp.MustCompile(`\sAdd\s+\d+\s+\d+\s+\S+\s+_luminex\._tcp\.\s+(.+?)\s*$`)
var reachedRe = regexp.MustCompile(`(?i)can be reached at\s+(\S+?):(\d+)`)

func dnsSdBrowse(d time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dns-sd", "-B", Service, "local")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dns-sd: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot run dns-sd: %w", err)
	}
	seen := map[string]bool{}
	var names []string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if m := dnsSdAddRe.FindStringSubmatch(sc.Text()); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	_ = cmd.Wait()
	return names, nil
}

func dnsSdResolve(name string, d time.Duration) *rawService {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dns-sd", "-L", name, Service, "local")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	var res *rawService
	sc := bufio.NewScanner(out)
	var host string
	var port int
	for sc.Scan() {
		line := sc.Text()
		if m := reachedRe.FindStringSubmatch(line); m != nil {
			host = m[1]
			fmt.Sscanf(m[2], "%d", &port)
			continue
		}
		if host != "" && strings.TrimSpace(line) != "" {
			res = &rawService{Instance: name, Host: host, Port: port, Txt: parseDnsSdTxt(line), RawTxt: strings.TrimSpace(line)}
			break
		}
	}
	cancel()
	_ = cmd.Wait()
	return res
}

func dnsSdScan(d time.Duration) ([]rawService, error) {
	names, err := dnsSdBrowse(d)
	if err != nil {
		return nil, err
	}
	Logf("dns-sd browse: %d instance(s) %q", len(names), names)
	results := make([]*rawService, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			results[i] = dnsSdResolve(n, 4*time.Second)
		}(i, n)
	}
	wg.Wait()
	var out []rawService
	for i, r := range results {
		if r == nil {
			Warnf("dns-sd could not resolve %q", names[i])
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// ───────────────────────── public ─────────────────────────

// Scan returns devices built purely from TXT records. The error is non-nil
// only when no backend could browse at all (the native socket could not bind
// and dns-sd is missing or would not start): the caller should show it,
// because "no devices" would be the wrong story. A scan that ran and found
// nothing returns (nil, nil).
func Scan(d time.Duration) ([]Device, error) {
	var raw []rawService
	var err error
	switch chooseBackend() {
	case "dns-sd":
		raw, err = dnsSdScan(d)
	default:
		raw, err = mdnsScan(d)
		if err != nil {
			Warnf("native mDNS failed: %v", err)
			if !haveDnsSd() {
				return nil, fmt.Errorf("%v, and Bonjour (dns-sd) is not installed, so this computer cannot browse for devices. Close other mDNS software or install Bonjour, then rescan", err)
			}
			setBackend("dns-sd", "native socket could not bind UDP 5353; using Bonjour's dns-sd")
			raw, err = dnsSdScan(d)
		}
	}
	if err != nil {
		Warnf("scan failed: %v", err)
		return nil, err
	}
	seen := map[string]bool{}
	var devs []Device
	for _, r := range raw {
		d := ParseTxt(r.Txt)
		d.Instance, d.Host, d.MdnsPort, d.RawTxt = r.Instance, r.Host, r.Port, r.RawTxt
		d.ID = deviceID(d)
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		devs = append(devs, d)
	}
	// Stable row order: by type, then serial.
	sort.Slice(devs, func(a, b int) bool {
		if devs[a].TypeLabel != devs[b].TypeLabel {
			return devs[a].TypeLabel < devs[b].TypeLabel
		}
		return devs[a].Serial < devs[b].Serial
	})
	Logf("scan: %d device(s)", len(devs))
	for _, d := range devs {
		Logf("  %-16s %-14s %-14s %-15s %q fw=%s", d.TypeLabel, d.Model, d.Serial, d.IP, d.Name, d.Firmware)
	}
	return devs, nil
}

// deviceID is what identifies a device across a reboot or an IP change: the
// TXT serial, else the MAC, else the mDNS instance name. The real GigaCore
// 16tf and LumiNode 12 seen so far send all three (instance name = serial);
// the fallbacks cover a device or firmware that leaves serial and mac out.
func deviceID(d Device) string {
	if d.Serial != "" {
		return d.Serial
	}
	if d.MAC != "" {
		return d.MAC
	}
	return d.Instance
}

// SameDevice reports whether two scan results are the same physical device,
// whatever address or name it has now.
// DeviceFromFields builds a Device from TXT-style fields that came from somewhere
// other than mDNS, such as a device's own API.
func DeviceFromFields(kv map[string]string) Device {
	d := ParseTxt(kv)
	d.Instance = d.Serial
	d.ID = deviceID(d)
	return d
}

func SameDevice(a, b Device) bool {
	return a.ID != "" && a.ID == b.ID
}
