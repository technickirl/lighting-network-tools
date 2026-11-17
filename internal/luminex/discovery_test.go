package luminex

import (
	"testing"
	"time"
)

// The reader goroutine closing done must end the re-query loop at once,
// not leave it spinning on the closed channel until the deadline.
func TestRequeryStopsWhenReaderExits(t *testing.T) {
	done := make(chan struct{})
	close(done)
	sends := 0
	start := time.Now()
	requeryUntil(done, time.Now().Add(2*time.Second), time.Millisecond, func() { sends++ })
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("loop ran %v after the reader had exited", took)
	}
	if sends > 1 {
		t.Errorf("sent %d queries after the reader had exited", sends)
	}
}

func TestRequeryRunsToDeadline(t *testing.T) {
	done := make(chan struct{})
	sends := 0
	start := time.Now()
	requeryUntil(done, time.Now().Add(120*time.Millisecond), 20*time.Millisecond, func() { sends++ })
	if took := time.Since(start); took < 100*time.Millisecond || took > time.Second {
		t.Fatalf("loop ran %v, wanted about 120ms", took)
	}
	if sends < 2 {
		t.Errorf("only %d re-queries before the deadline", sends)
	}
}

func TestLostInterfaces(t *testing.T) {
	en0 := Iface{Name: "en0", Address: "100.105.182.186", Prefix: 21}
	lan := Iface{Name: "en11", Address: "192.168.100.56", Prefix: 16}
	alias := Iface{Name: "en11", Address: "2.99.99.99", Prefix: 8}
	good := []Iface{en0, lan, alias}
	if lost := LostInterfaces(good, good); len(lost) != 0 {
		t.Fatalf("nothing changed, got %+v", lost)
	}
	lost := LostInterfaces(good, []Iface{en0}) // cable out: en11 lost both addresses
	if len(lost) != 2 || lost[0].Address != "192.168.100.56" || lost[1].Address != "2.99.99.99" {
		t.Fatalf("expected both en11 addresses, got %+v", lost)
	}
	if lost := LostInterfaces(nil, []Iface{en0}); len(lost) != 0 {
		t.Fatalf("no history, got %+v", lost)
	}
}

func TestWatchInterfacesReportsAddedAndRemoved(t *testing.T) {
	a := Iface{Name: "en0", Address: "192.168.1.250", Prefix: 16}
	b := Iface{Name: "en7", Address: "2.0.0.250", Prefix: 8}
	reads := [][]Iface{{a}, {a}, {a, b}, {b}}
	n := 0
	stop := make(chan struct{})
	type change struct{ added, removed string }
	var got []change
	WatchInterfaces(stop, func() []Iface {
		cur := reads[n]
		if n < len(reads)-1 {
			n++
		} else {
			select {
			case <-stop:
			default:
				close(stop)
			}
		}
		return cur
	}, time.Millisecond, func(added, removed []Iface) {
		got = append(got, change{DescribeInterfaces(added), DescribeInterfaces(removed)})
	})
	want := []change{{"en7 2.0.0.250/8", ""}, {"", "en0 192.168.1.250/16"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
