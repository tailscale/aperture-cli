package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestLaunchState_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg == "" {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	}

	want := config.LaunchState{
		LastClientName:  "Claude Code",
		LastBackendType: "bedrock",
		LastProviderID:  "anthropic-via-aperture",
		LastModel:       "anthropic-via-aperture/claude-sonnet",
	}
	if err := config.SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := config.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if got != want {
		t.Errorf("LoadState = %+v, want %+v", got, want)
	}
}

func TestLaunchState_LegacyMigration(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	// Seed a launcher.json in the old shape that used lastProfileName.
	dir := filepath.Join(tmp, ".config", "aperture")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]string{
		"lastProfileName": "Claude Code",
		"lastBackendType": "anthropic",
		"lastProviderId":  "anthropic-via-aperture",
		"lastModel":       "anthropic-via-aperture/claude-sonnet",
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "launcher.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := config.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.LastClientName != "Claude Code" {
		t.Errorf("LastClientName = %q, want %q", got.LastClientName, "Claude Code")
	}
	if got.LastProviderID != "anthropic-via-aperture" {
		t.Errorf("LastProviderID = %q", got.LastProviderID)
	}
}

func TestSettings_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	want := config.Settings{
		Portals: []config.Portal{
			{ID: "portal-abcdef", Name: "Work"},
		},
		Endpoints: []config.Endpoint{
			{URL: "http://ai"},
			{URL: "http://aperture.example.com", PortalID: "portal-abcdef"},
		},
		YoloMode: true,
	}
	if err := config.SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	got, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(got.Endpoints) != 2 || got.Endpoints[0].URL != "http://ai" {
		t.Errorf("endpoints = %+v", got.Endpoints)
	}
	if len(got.Portals) != 1 || got.Portals[0].ID != "portal-abcdef" {
		t.Errorf("portals = %+v", got.Portals)
	}
	if got.Endpoints[1].PortalID != "portal-abcdef" {
		t.Errorf("portal endpoint = %+v", got.Endpoints[1])
	}
	if !got.YoloMode {
		t.Error("YoloMode = false, want true")
	}
}

func TestGlobal_SetApertureHost_RotatesToFront(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	g := &config.Global{
		Settings: config.Settings{
			Endpoints: []config.Endpoint{
				{URL: "http://a"},
				{URL: "http://b"},
				{URL: "http://c"},
			},
		},
	}
	if err := g.SetApertureHost("http://b"); err != nil {
		t.Fatal(err)
	}
	if g.ApertureHost != "http://b" {
		t.Errorf("ApertureHost = %q", g.ApertureHost)
	}
	if g.Settings.Endpoints[0].URL != "http://b" {
		t.Errorf("front endpoint = %q, want http://b", g.Settings.Endpoints[0].URL)
	}
	if len(g.Settings.Endpoints) != 3 {
		t.Errorf("endpoints len = %d, want 3", len(g.Settings.Endpoints))
	}
}

func TestGlobal_SetActiveEndpoint_DistinguishesPortal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	g := &config.Global{
		Settings: config.Settings{
			Endpoints: []config.Endpoint{
				{URL: "http://ai"},
				{URL: "http://ai", PortalID: "portal-abcdef"},
			},
		},
	}
	if err := g.SetActiveEndpoint(config.Endpoint{URL: "http://ai", PortalID: "portal-abcdef"}); err != nil {
		t.Fatal(err)
	}
	if g.Settings.Endpoints[0].PortalID != "portal-abcdef" {
		t.Errorf("front endpoint = %+v, want portal endpoint", g.Settings.Endpoints[0])
	}
	if len(g.Settings.Endpoints) != 2 {
		t.Errorf("endpoints len = %d, want 2", len(g.Settings.Endpoints))
	}
}

func TestPortalStateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	got, err := config.PortalStateDir("portal-abcdef")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".config", "aperture", "portals", "abcdef")
	if got != want {
		t.Errorf("PortalStateDir = %q, want %q", got, want)
	}
}

func TestClientConfig_TypedStore(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	type myCfg struct {
		Foo string `json:"foo"`
		N   int    `json:"n"`
	}

	store, err := config.ClientConfig[myCfg]("test-client")
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got != (myCfg{}) {
		t.Errorf("Load on missing file = %+v, want zero", got)
	}

	want := myCfg{Foo: "bar", N: 42}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err = store.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}
