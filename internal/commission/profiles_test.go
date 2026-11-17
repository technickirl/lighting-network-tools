package commission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withProfilesFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub", "group-profiles.json")
	old := profilesPath
	profilesPath = func() string { return p }
	t.Cleanup(func() { profilesPath = old })
	return p
}

// A profile with the same groups on a 10, a 12 and a 16 port switch, each with its own ports.
func multiProfile() SavedProfile {
	return SavedProfile{Name: "FOH rack",
		Groups: []SavedGroup{{Nr: 2, Name: "Lights", VID: 200}, {Nr: 3, Name: "Video", VID: 300}},
		Layouts: []Layout{
			{PortCount: 12, ISL: &ProfileISL{Ports: []int{11, 12}}, Groups: []LayoutGroup{{Nr: 2, Ports: []int{1, 2}}, {Nr: 3, Ports: []int{3, 4}, Tagged: []int{5}}}},
			{PortCount: 10, Groups: []LayoutGroup{{Nr: 2, Ports: []int{4, 3}}, {Nr: 3, Ports: []int{1}}}},
			{PortCount: 16, Groups: []LayoutGroup{{Nr: 2, Ports: []int{1, 2, 3, 4}}}},
		},
	}
}

func TestSavedProfiles(t *testing.T) {
	path := withProfilesFile(t)
	if all, err := loadProfiles(); err != nil || len(all) != 0 {
		t.Fatalf("no file yet: %v %v", all, err)
	}
	if err := saveProfile(multiProfile()); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(SavedProfile{Name: "Stage", Groups: []SavedGroup{{Nr: 3, Name: "Video", VID: 300}}, Layouts: []Layout{{PortCount: 12, Groups: []LayoutGroup{{Nr: 3, Ports: []int{1}}}}}}); err != nil {
		t.Fatal(err)
	}
	all, _ := loadProfiles()
	if len(all) != 2 || all[0].Name != "FOH rack" || len(all[0].Layouts) != 3 {
		t.Fatalf("saved and sorted wrong: %+v", all)
	}
	// Layouts are kept by size, smallest first, with sorted ports; the 16-port one has no group 3 entry and still resolves.
	if all[0].Layouts[0].PortCount != 10 || joinInts(all[0].Layouts[0].Groups[0].Ports) != "3,4" {
		t.Errorf("layouts not sorted/cleaned: %+v", all[0].Layouts)
	}
	r := all[0].resolve(all[0].Layouts[2])
	if r.PortCount != 16 || len(r.Groups) != 2 || joinInts(r.Groups[0].Ports) != "1,2,3,4" || len(r.Groups[1].Ports) != 0 {
		t.Errorf("resolve for 16 ports: %+v", r)
	}
	r = all[0].resolve(all[0].Layouts[1])
	if r.ISL == nil || joinInts(r.ISL.Ports) != "11,12" || r.Groups[1].Name != "Video" {
		t.Errorf("resolve for 12 ports: %+v", r)
	}
	// Saving under the same name (any case) replaces it.
	p := multiProfile()
	p.Name = "foh RACK"
	p.Groups[0].Name = "Lights 2"
	if err := saveProfile(p); err != nil {
		t.Fatal(err)
	}
	if all, _ = loadProfiles(); len(all) != 2 || all[0].Groups[0].Name != "Lights 2" {
		t.Errorf("replace: %+v", all)
	}
	// A profile that could not be applied cannot be saved either.
	bad := map[string]func(*SavedProfile){
		"no name":             func(s *SavedProfile) { s.Name = "" },
		"no groups":           func(s *SavedProfile) { s.Groups = nil },
		"no layouts":          func(s *SavedProfile) { s.Layouts = nil },
		"two 12-port layouts": func(s *SavedProfile) { s.Layouts = append(s.Layouts, Layout{PortCount: 12}) },
		"port out of range":   func(s *SavedProfile) { s.Layouts[1].Groups[0].Ports = []int{99} },
		"undefined group": func(s *SavedProfile) {
			s.Layouts[1].Groups = append(s.Layouts[1].Groups, LayoutGroup{Nr: 9, Ports: []int{1}})
		},
		"port in two groups":  func(s *SavedProfile) { s.Layouts[0].Groups[1].Ports = []int{1} },
		"isl port in a group": func(s *SavedProfile) { s.Layouts[0].ISL.Ports = []int{1} },
		"bad group name":      func(s *SavedProfile) { s.Groups[0].Name = "a/b" },
		"same vid twice":      func(s *SavedProfile) { s.Groups[1].VID = 200 },
	}
	for what, mutate := range bad {
		p := multiProfile()
		mutate(&p)
		if err := saveProfile(p); err == nil {
			t.Errorf("%s should be refused", what)
		}
	}
	if err := deleteProfile("STAGE"); err != nil {
		t.Fatal(err)
	}
	if all, _ = loadProfiles(); len(all) != 1 {
		t.Errorf("delete: %+v", all)
	}
	if deleteProfile("nope") == nil {
		t.Error("deleting a missing profile should say so")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temporary file should be gone")
	}
	os.WriteFile(path, []byte("not json"), 0o644)
	if _, err := loadProfiles(); err == nil {
		t.Error("a damaged file should be reported, not silently replaced")
	}
}

// Profiles written by the earlier single-size format are read as a profile with one layout.
func TestOlderProfileFileIsMigrated(t *testing.T) {
	path := withProfilesFile(t)
	os.MkdirAll(filepath.Dir(path), 0o755)
	old := `[{"name":"Switch/Node Default","portCount":10,"isl":{"ports":[6,7,8]},"groups":[{"nr":2,"name":"Art-Net","vid":200,"ports":[],"tagged":[]},{"nr":3,"name":"sACN","vid":300,"ports":[3,4],"tagged":[]}]}]`
	os.WriteFile(path, []byte(old), 0o644)
	all, err := loadProfiles()
	if err != nil || len(all) != 1 {
		t.Fatalf("load: %v %+v", err, all)
	}
	p := all[0]
	if len(p.Layouts) != 1 || p.Layouts[0].PortCount != 10 || joinInts(p.Layouts[0].ISL.Ports) != "6,7,8" || joinInts(p.Layouts[0].Groups[1].Ports) != "3,4" || p.Groups[1].Name != "sACN" {
		t.Errorf("migrated: %+v", p)
	}
	// It applies exactly as before.
	if r := p.resolve(p.Layouts[0]); r.PortCount != 10 || joinInts(r.Groups[1].Ports) != "3,4" {
		t.Errorf("resolve: %+v", r)
	}
	// Saving the older format over the API still works and is stored in the new one.
	if err := saveProfile(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"layouts"`) {
		t.Errorf("the file should now be in the new format: %s", b)
	}
}
