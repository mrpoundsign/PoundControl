package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

func TestManifestValidate(t *testing.T) {
	valid := Manifest{
		UUID:     "com.github.mrpoundsign.poundcontrol.obs",
		Name:     "OBS Studio",
		Version:  "0.1.0",
		Provider: ProviderBuiltin,
		Actions: []ActionDescriptor{
			{
				UUID:        "com.github.mrpoundsign.poundcontrol.obs.scene",
				PluginUUID:  "com.github.mrpoundsign.poundcontrol.obs",
				Name:        "Switch Scene",
				Category:    "OBS Studio",
				Controllers: []deck.ControllerType{deck.ControllerKeypad},
				Provider:    ProviderBuiltin,
			},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest returned unexpected error: %v", err)
	}

	invalidCases := []struct {
		name     string
		manifest Manifest
	}{
		{
			name: "missing UUID",
			manifest: Manifest{
				Name:     "No UUID",
				Provider: ProviderBuiltin,
			},
		},
		{
			name: "missing Name",
			manifest: Manifest{
				UUID:     "com.example.noname",
				Provider: ProviderBuiltin,
			},
		},
		{
			name: "missing Provider",
			manifest: Manifest{
				UUID: "com.example.noprovider",
				Name: "No Provider",
			},
		},
		{
			name: "empty action UUID",
			manifest: Manifest{
				UUID:     "com.example.badact",
				Name:     "Bad Action",
				Provider: ProviderStreamDeck,
				Actions: []ActionDescriptor{
					{Name: "Unnamed"},
				},
			},
		},
		{
			name: "empty action Name",
			manifest: Manifest{
				UUID:     "com.example.badactname",
				Name:     "Bad Action Name",
				Provider: ProviderWasm,
				Actions: []ActionDescriptor{
					{UUID: "com.example.act"},
				},
			},
		},
		{
			name: "duplicate action UUID",
			manifest: Manifest{
				UUID:     "com.example.dup",
				Name:     "Duplicate Actions",
				Provider: ProviderBuiltin,
				Actions: []ActionDescriptor{
					{UUID: "com.example.same", Name: "First"},
					{UUID: "com.example.same", Name: "Second"},
				},
			},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.manifest.Validate(); err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

func TestActionDescriptorSupportsController(t *testing.T) {
	defaultAct := ActionDescriptor{UUID: "a", Name: "Default Keypad"}
	if !defaultAct.SupportsController(deck.ControllerKeypad) {
		t.Error("expected empty Controllers to default to supporting Keypad")
	}
	if defaultAct.SupportsController(deck.ControllerEncoder) {
		t.Error("expected empty Controllers not to support Encoder")
	}

	dialAct := ActionDescriptor{
		UUID:        "b",
		Name:        "Master Volume",
		Controllers: []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
	}
	if !dialAct.SupportsController(deck.ControllerKeypad) || !dialAct.SupportsController(deck.ControllerEncoder) {
		t.Error("expected dual controller action to support both Keypad and Encoder")
	}
}

func TestNormalizeSectionAndSchemaJSON(t *testing.T) {
	cases := []struct {
		raw       string
		wantTitle string
		wantCanon bool
	}{
		{"", SectionGeneral, true},
		{"   ", SectionGeneral, true},
		{"general", SectionGeneral, true},
		{" General ", SectionGeneral, true},
		{"APPEARANCE", SectionAppearance, true},
		{"trigger", SectionTrigger, true},
		{"Advanced", SectionAdvanced, true},
		{"  Timer  ", "Timer", false},
		{"Thresholds", "Thresholds", false},
	}
	for _, tc := range cases {
		gotTitle, gotCanon := NormalizeSection(tc.raw)
		if gotTitle != tc.wantTitle || gotCanon != tc.wantCanon {
			t.Errorf("NormalizeSection(%q) = (%q, %v), want (%q, %v)", tc.raw, gotTitle, gotCanon, tc.wantTitle, tc.wantCanon)
		}
	}

	withoutSec := SettingField{Key: "x", Label: "X", Type: FieldText}
	rawNoSec, err := json.Marshal(withoutSec)
	if err != nil {
		t.Fatalf("Marshal(withoutSec) error = %v", err)
	}
	if strings.Contains(string(rawNoSec), "section") {
		t.Fatalf("expected omitempty on empty Section, got %s", rawNoSec)
	}

	withSec := SettingField{Key: "doneColor", Label: "Done", Type: FieldColor, Section: SectionAppearance}
	rawWithSec, err := json.Marshal(withSec)
	if err != nil {
		t.Fatalf("Marshal(withSec) error = %v", err)
	}
	var decoded SettingField
	if err := json.Unmarshal(rawWithSec, &decoded); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if decoded.Section != SectionAppearance {
		t.Fatalf("round-trip Section = %q, want %q", decoded.Section, SectionAppearance)
	}
}
