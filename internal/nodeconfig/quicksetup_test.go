package nodeconfig

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// quickNode answers like a node for Quick Setup: a new IO gets the next id, everything else is accepted. It keeps
// the input each block was given and how many IOs were created.
func quickNode(t *testing.T) (ip string, inputs map[string]string, created *int) {
	t.Helper()
	inputs, created = map[string]string{}, new(int)
	next := 50
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/IO":
			*created++
			fmt.Fprintf(w, `{"index":%d}`, next)
			next++
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/input/0"):
			inputs[r.URL.Path] = string(b)
			w.WriteHeader(204)
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), inputs, created
}

// Two ports already set to U1 and U2, as Quick Setup leaves them.
func quickState() (ios, blocks []obj) {
	ios = []obj{
		{"id": 0.0, "io_class": "dmx", "io_type": "output", "port_number": 0.0},
		{"id": 1.0, "io_class": "dmx", "io_type": "output", "port_number": 1.0},
		{"id": 10.0, "io_class": "sacn", "io_type": "input", "universe": 1.0},
		{"id": 11.0, "io_class": "sacn", "io_type": "input", "universe": 2.0},
	}
	blocks = []obj{
		{"id": 0.0, "inputs": obj{"0": 10.0}, "outputs": obj{"0": 0.0}},
		{"id": 1.0, "inputs": obj{"0": 11.0}, "outputs": obj{"1": 1.0}},
	}
	return
}

// The node refuses an IO another block holds, and Quick Setup has cleared the block by then. Moving port 1 to the
// universe port 2 is on must therefore make a new IO, not take port 2's.
func TestQuickSetupNeverTakesAnotherBlocksIO(t *testing.T) {
	ip, inputs, created := quickNode(t)
	ios, blocks := quickState()
	held := attachedIOs(blocks)
	if err := quickSetupPort(ip, 0, 1, 2, &ios, blocks, held); err != nil {
		t.Fatal(err)
	}
	if got := inputs["/api/processblock/0/input/0"]; got != `{"io_id":50}` || *created != 1 {
		t.Errorf("port 1 input = %s, created = %d; wanted a new IO (50)", got, *created)
	}
	// Port 2 moves on to U3: nothing holds one, so another new IO. Port 1's old U1 IO is free again by now.
	if err := quickSetupPort(ip, 1, 2, 3, &ios, blocks, held); err != nil {
		t.Fatal(err)
	}
	if held[10] || !held[50] || !held[51] {
		t.Errorf("held = %v, wanted 50 and 51 held and 10 free", held)
	}
}

func TestQuickSetupReusesItsOwnAndFreeIOs(t *testing.T) {
	ip, inputs, created := quickNode(t)
	ios, blocks := quickState()
	held := attachedIOs(blocks)
	if err := quickSetupPort(ip, 0, 1, 1, &ios, blocks, held); err != nil { // same universe again
		t.Fatal(err)
	}
	if got := inputs["/api/processblock/0/input/0"]; got != `{"io_id":10}` || *created != 0 {
		t.Errorf("input = %s, created = %d; wanted its own IO 10 reused", got, *created)
	}
}

// Two ports put on one universe in the same run: the second cannot share the first's IO.
func TestQuickSetupTwoPortsOnOneUniverse(t *testing.T) {
	ip, inputs, created := quickNode(t)
	ios, blocks := quickState()
	held := attachedIOs(blocks)
	for port := 1; port <= 2; port++ {
		if err := quickSetupPort(ip, port-1, port, 7, &ios, blocks, held); err != nil {
			t.Fatal(err)
		}
	}
	a, b := inputs["/api/processblock/0/input/0"], inputs["/api/processblock/1/input/0"]
	if a == b || *created != 2 {
		t.Errorf("inputs %s and %s, created %d; wanted an IO each", a, b, *created)
	}
}
