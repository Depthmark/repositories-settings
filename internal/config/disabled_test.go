package config

import (
	"reflect"
	"testing"
)

func TestParseDisabledResources_NormalizesAndAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"pages", []string{"pages"}},
		{"pages,secrets", []string{"pages", "secrets"}},
		{" Pages , Secrets ", []string{"pages", "secrets"}},
		// Hyphen and underscore both map to the canonical snake_case key.
		{"deploy-keys", []string{"deploy_keys"}},
		{"deploy_keys", []string{"deploy_keys"}},
		{"Custom-Properties", []string{"custom_properties"}},
		// Duplicates collapse.
		{"pages,pages,Pages", []string{"pages"}},
	}
	for _, tc := range cases {
		got, unknown := ParseDisabledResources(tc.in)
		if len(unknown) != 0 {
			t.Errorf("ParseDisabledResources(%q) had unexpected unknowns %v", tc.in, unknown)
		}
		gotKeys := got.Keys()
		if !reflect.DeepEqual(gotKeys, tc.want) {
			t.Errorf("ParseDisabledResources(%q) = %v, want %v", tc.in, gotKeys, tc.want)
		}
	}
}

func TestParseDisabledResources_ReturnsUnknownsForTypos(t *testing.T) {
	got, unknown := ParseDisabledResources("pages,nonexistent,secrets,fr0bnicate")
	if !got.Has("pages") || !got.Has("secrets") {
		t.Fatalf("known keys lost: %v", got.Keys())
	}
	if len(unknown) != 2 || unknown[0] != "nonexistent" || unknown[1] != "fr0bnicate" {
		t.Fatalf("expected the two typos to be returned, got %v", unknown)
	}
}

func TestDisabledResources_HasNilSafe(t *testing.T) {
	var d DisabledResources
	if d.Has("pages") {
		t.Fatal("nil DisabledResources.Has must return false")
	}
}

func TestIsResourceConfigured_TopicsRideRepository(t *testing.T) {
	s := &Settings{Topics: &[]string{"go"}}
	if !IsResourceConfigured(s, "repository") {
		t.Fatal("topics-only Settings should report repository as configured")
	}
}

func TestIsResourceConfigured_PerSection(t *testing.T) {
	cases := []struct {
		key  string
		s    *Settings
		want bool
	}{
		{"pages", &Settings{Pages: &PagesConfig{}}, true},
		{"pages", &Settings{}, false},
		{"secrets", &Settings{Secrets: &SecretsConfig{}}, true},
		{"deploy_keys", &Settings{DeployKeys: &DeployKeysConfig{}}, true},
		{"deploy_keys", &Settings{}, false},
		{"unknown", &Settings{Pages: &PagesConfig{}}, false}, // bad key
		{"pages", nil, false},                                // nil Settings
	}
	for _, tc := range cases {
		if got := IsResourceConfigured(tc.s, tc.key); got != tc.want {
			t.Errorf("IsResourceConfigured(%v, %q) = %v, want %v", tc.s, tc.key, got, tc.want)
		}
	}
}
