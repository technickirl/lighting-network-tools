package commission

// Saved group profiles: layouts of a switch's groups (see groups.go) that the app remembers between runs, so nobody
// has to hunt for JSON files. A profile holds its groups once (number, name, VLAN ID) and a port layout for each switch
// size (port count): which ports are untagged or tagged in each group, and optionally which are ISL links. Applying it
// to a switch uses the layout for that switch's port count. They live in one file in the user's config folder, keyed by name:
// ~/Library/Application Support/luminex-commission/group-profiles.json on a Mac, %AppData% on Windows,
// ~/.config on Linux. LUMINEX_PROFILES_FILE overrides the path.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var profilesMu sync.Mutex

// profilesPath is a variable so tests can point it at a temporary file.
var profilesPath = func() string {
	if p := os.Getenv("LUMINEX_PROFILES_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "luminex-commission", "group-profiles.json")
}

// SavedGroup is a group's definition, the same on every switch size.
type SavedGroup struct {
	Nr   int    `json:"nr"`
	Name string `json:"name"`
	VID  int    `json:"vid"`
}

// LayoutGroup is which ports are in a group on one switch size.
type LayoutGroup struct {
	Nr     int   `json:"nr"`
	Ports  []int `json:"ports"`
	Tagged []int `json:"tagged"`
}

// Layout is the port assignment for switches with PortCount ports.
type Layout struct {
	PortCount int           `json:"portCount"`
	ISL       *ProfileISL   `json:"isl,omitempty"`
	Groups    []LayoutGroup `json:"groups"`
}

// SavedProfile is what the app stores: shared groups and a layout per switch size.
type SavedProfile struct {
	Name    string       `json:"name"`
	Groups  []SavedGroup `json:"groups"`
	Layouts []Layout     `json:"layouts"`
}

// resolve turns one layout into the single-size profile that applying works with.
func (s SavedProfile) resolve(l Layout) GroupProfile {
	p := GroupProfile{Name: s.Name, PortCount: l.PortCount, ISL: l.ISL}
	byNr := map[int]LayoutGroup{}
	for _, g := range l.Groups {
		byNr[g.Nr] = g
	}
	for _, g := range s.Groups {
		lg := byNr[g.Nr]
		p.Groups = append(p.Groups, ProfileGroup{Nr: g.Nr, Name: g.Name, VID: g.VID, Ports: lg.Ports, Tagged: lg.Tagged})
	}
	return p
}

// validate checks the profile as a whole: every layout must be a usable profile on its own, and the layouts must be
// for different sizes. A layout may only mention groups the profile defines.
func (s *SavedProfile) validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 60 {
		return fmt.Errorf("a profile needs a name of up to 60 characters")
	}
	if len(s.Groups) == 0 {
		return fmt.Errorf("add at least one group")
	}
	if len(s.Layouts) == 0 {
		return fmt.Errorf("add a port layout for at least one switch size")
	}
	defined := map[int]bool{}
	for _, g := range s.Groups {
		defined[g.Nr] = true
	}
	sizes := map[int]bool{}
	for i := range s.Layouts {
		l := &s.Layouts[i]
		if sizes[l.PortCount] {
			return fmt.Errorf("there are two layouts for %d-port switches", l.PortCount)
		}
		sizes[l.PortCount] = true
		for _, g := range l.Groups {
			if !defined[g.Nr] {
				return fmt.Errorf("the %d-port layout uses group %d, which the profile does not define", l.PortCount, g.Nr)
			}
		}
		b, _ := json.Marshal(s.resolve(*l))
		checked, err := parseGroupProfile(string(b))
		if err != nil {
			return fmt.Errorf("%d-port layout: %w", l.PortCount, err)
		}
		// keep the cleaned (sorted, non-nil) ports
		l.Groups = l.Groups[:0]
		for _, g := range checked.Groups {
			l.Groups = append(l.Groups, LayoutGroup{Nr: g.Nr, Ports: g.Ports, Tagged: g.Tagged})
		}
		l.ISL = checked.ISL
	}
	sort.Slice(s.Layouts, func(i, j int) bool { return s.Layouts[i].PortCount < s.Layouts[j].PortCount })
	sort.Slice(s.Groups, func(i, j int) bool { return s.Groups[i].Nr < s.Groups[j].Nr })
	return nil
}

// fromSingle converts a profile saved by an earlier version (one port count, ports inside each group).
func fromSingle(p GroupProfile) SavedProfile {
	s := SavedProfile{Name: p.Name}
	l := Layout{PortCount: p.PortCount, ISL: p.ISL}
	for _, g := range p.Groups {
		s.Groups = append(s.Groups, SavedGroup{Nr: g.Nr, Name: g.Name, VID: g.VID})
		l.Groups = append(l.Groups, LayoutGroup{Nr: g.Nr, Ports: g.Ports, Tagged: g.Tagged})
	}
	s.Layouts = []Layout{l}
	return s
}

// decodeProfile reads either format.
func decodeProfile(b []byte) (SavedProfile, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return SavedProfile{}, err
	}
	if _, ok := probe["layouts"]; ok {
		var s SavedProfile
		return s, json.Unmarshal(b, &s)
	}
	var p GroupProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return SavedProfile{}, err
	}
	return fromSingle(p), nil
}

func loadProfiles() ([]SavedProfile, error) {
	b, err := os.ReadFile(profilesPath())
	if os.IsNotExist(err) {
		return []SavedProfile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s is not a list of profiles: %w", profilesPath(), err)
	}
	all := []SavedProfile{}
	for _, r := range raw {
		s, err := decodeProfile(r)
		if err != nil {
			return nil, fmt.Errorf("%s has a profile that cannot be read: %w", profilesPath(), err)
		}
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, nil
}

// writeProfiles replaces the file in one step, so a crash cannot leave half a file.
func writeProfiles(all []SavedProfile) error {
	p := profilesPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// saveProfile adds or replaces the profile with that name (names are compared ignoring case).
func saveProfile(p SavedProfile) error {
	profilesMu.Lock()
	defer profilesMu.Unlock()
	if err := p.validate(); err != nil {
		return err
	}
	all, err := loadProfiles()
	if err != nil {
		return err
	}
	out := []SavedProfile{p}
	for _, o := range all {
		if !strings.EqualFold(o.Name, p.Name) {
			out = append(out, o)
		}
	}
	return writeProfiles(out)
}

func deleteProfile(name string) error {
	profilesMu.Lock()
	defer profilesMu.Unlock()
	all, err := loadProfiles()
	if err != nil {
		return err
	}
	var out []SavedProfile
	for _, o := range all {
		if !strings.EqualFold(o.Name, name) {
			out = append(out, o)
		}
	}
	if len(out) == len(all) {
		return fmt.Errorf("there is no saved profile called %q", name)
	}
	return writeProfiles(out)
}

func handleProfilesList(w http.ResponseWriter, r *http.Request) {
	profilesMu.Lock()
	all, err := loadProfiles()
	profilesMu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": all, "path": profilesPath()})
}

func handleProfilesSave(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var p SavedProfile
	if err == nil {
		p, err = decodeProfile(b) // the page sends the current format; the older single-size one is still accepted
	}
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "That is not a profile."})
		return
	}
	if err := saveProfile(p); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	handleProfilesList(w, r)
}

func handleProfilesDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = readJSON(r, &body)
	if err := deleteProfile(body.Name); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	handleProfilesList(w, r)
}
