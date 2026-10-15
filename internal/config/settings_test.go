package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// A bridge written before ports existed must load with no port, and a port
// must survive the round trip.
func TestBridgePortRoundTrip(t *testing.T) {
	var old Bridge
	if err := json.Unmarshal([]byte(`{"id":"bridge-abc123","name":"Work"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Port != 0 {
		t.Errorf("old record gained a port: %+v", old)
	}
	data, err := json.Marshal(Bridge{ID: "bridge-abc123", Name: "Work", Port: 8991})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"id":"bridge-abc123","name":"Work","port":8991}` {
		t.Errorf("marshal = %s", data)
	}
	var back Bridge
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Port != 8991 {
		t.Errorf("port = %d after round trip", back.Port)
	}
}

func TestUseSettingsFile(t *testing.T) {
	defer func() { settingsFile = "" }()
	path := filepath.Join(t.TempDir(), "elsewhere.json")
	UseSettingsFile(path)

	if err := SaveSettings(Settings{Bridges: []Bridge{{ID: "bridge-abc123", Name: "Work"}}}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(got.Bridges) != 1 || got.Bridges[0].Name != "Work" {
		t.Errorf("bridges = %+v, want the one saved at the override path", got.Bridges)
	}
	p, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != path {
		t.Errorf("settingsPath = %q, want %q", p, path)
	}
}

// An endpoint list written before names existed must load with empty names,
// and a name must survive the round trip.
func TestEndpointNameRoundTrip(t *testing.T) {
	var old endpointList
	if err := old.UnmarshalJSON([]byte(`[{"url":"http://ai"}]`)); err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || old[0].Name != "" || old[0].Endpoint != Direct("http://ai") {
		t.Errorf("old record = %#v", old)
	}
	list := endpointList{{Endpoint: Direct("http://ai"), Name: "e1"}}
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `[{"name":"e1","url":"http://ai"}]` {
		t.Errorf("marshal = %s", data)
	}
	var back endpointList
	if err := back.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Name != "e1" || back[0].Endpoint != Direct("http://ai") {
		t.Errorf("decoded = %#v", back)
	}
}
