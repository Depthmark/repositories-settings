package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// yaml.v3 loads yes, on and a quoted "yes" into a Go bool as true, and
// refuses "true", 1 and maybe by line only. Every one of them is
// refused with the file, the line and the field.
func TestLoadFromMap_BranchBooleansAreTrueOrFalse(t *testing.T) {
	const want = "branches.yml: line 4: branches[0].lock_branch: must be true or false"
	for _, value := range []string{"yes", "on", `"yes"`, `"true"`, "1", "maybe"} {
		_, err := LoadFromMap(map[string][]byte{
			"branches.yml": []byte("_version: 1\nbranches:\n  - pattern: main\n    lock_branch: " + value + "\n"),
		})
		if err == nil || err.Error() != want {
			t.Errorf("lock_branch: %s: got %v, want %q", value, err, want)
		}
	}
	for value, want := range map[string]bool{"true": true, "false": false} {
		s, err := LoadFromMap(map[string][]byte{
			"branches.yml": []byte("_version: 1\nbranches:\n  - pattern: main\n    lock_branch: " + value + "\n"),
		})
		if err != nil {
			t.Fatalf("lock_branch: %s must load: %v", value, err)
		}
		if got := s.Branches.Branches[0].LockBranch; got == nil || *got != want {
			t.Errorf("lock_branch: %s loaded as %v", value, got)
		}
	}
}

// envelopes is what mergeFile decodes each settings file into.
var envelopes = map[string]reflect.Type{
	"repo.yml":              reflect.TypeFor[RepoFile](),
	"teams.yml":             reflect.TypeFor[TeamsFile](),
	"rulesets.yml":          reflect.TypeFor[RulesetsFile](),
	"branches.yml":          reflect.TypeFor[BranchesFile](),
	"environments.yml":      reflect.TypeFor[EnvironmentsFile](),
	"webhooks.yml":          reflect.TypeFor[WebhooksFile](),
	"autolinks.yml":         reflect.TypeFor[AutolinksFile](),
	"actions.yml":           reflect.TypeFor[ActionsFile](),
	"security.yml":          reflect.TypeFor[SecurityFile](),
	"pages.yml":             reflect.TypeFor[PagesFile](),
	"secrets.yml":           reflect.TypeFor[SecretsFile](),
	"variables.yml":         reflect.TypeFor[VariablesFile](),
	"deploy-keys.yml":       reflect.TypeFor[DeployKeysFile](),
	"custom-properties.yml": reflect.TypeFor[CustomPropertiesFile](),
	"collaborators.yml":     reflect.TypeFor[CollaboratorsFile](),
}

// Every bool field of every envelope, found by reflection, refuses yes
// with its path: a field added later is covered without a new test.
func TestLoadFromMap_EveryBooleanRefusesYes(t *testing.T) {
	var paths []string
	for _, name := range FileNames {
		envelope, ok := envelopes[name]
		if !ok {
			t.Fatalf("%s: add its envelope to envelopes", name)
		}
		for _, f := range boolFields(t, envelope, "", func(v any) any { return v }) {
			paths = append(paths, f.path)
			doc := f.doc.(map[string]any)
			doc["_version"] = 1
			raw, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.Replace(string(raw), boolMarker, "yes", 1))

			_, err = LoadFromMap(map[string][]byte{name: raw})
			var ve *ValidationError
			if !asValidationError(err, &ve) || len(ve.Issues) != 1 || !strings.HasSuffix(ve.Issues[0], ": "+f.path+": must be true or false") {
				t.Errorf("%s: %s: yes must be refused with its path, got %v\n%s", name, f.path, err, raw)
			}
		}
	}
	if !slices.Contains(paths, "branches[0].lock_branch") || !slices.Contains(paths, "environments[0].deployment_branch_policy.protected_branches") {
		t.Fatalf("the reflection walk missed known fields: %v", paths)
	}
	t.Logf("%d boolean fields refuse yes", len(paths))
}

const boolMarker = "__bool__"

type boolField struct {
	path string
	doc  any
}

// boolFields lists every bool and *bool under typ, each with the
// smallest document that sets only it to boolMarker. wrap nests a value
// at path.
func boolFields(t *testing.T, typ reflect.Type, path string, wrap func(any) any) []boolField {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if reflect.PointerTo(typ).Implements(reflect.TypeFor[yaml.Unmarshaler]()) {
		t.Fatalf("%s: %s decodes itself; check its booleans by hand", path, typ)
	}
	switch typ.Kind() {
	case reflect.Bool:
		return []boolField{{path, wrap(boolMarker)}}
	case reflect.Slice, reflect.Array:
		return boolFields(t, typ.Elem(), path+"[0]", func(v any) any { return wrap([]any{v}) })
	case reflect.Map:
		return boolFields(t, typ.Elem(), path+".x", func(v any) any { return wrap(map[string]any{"x": v}) })
	case reflect.Struct:
		var out []boolField
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			if strings.Contains(opts, "inline") {
				t.Fatalf("%s: inline field %s: teach yamlstrict's walk and this test to follow it", path, f.Name)
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			out = append(out, boolFields(t, f.Type, fieldPath, func(v any) any { return wrap(map[string]any{name: v}) })...)
		}
		return out
	}
	return nil
}

// Free-form values keep the YAML value as written: GitHub, not the Go
// type, decides what a ruleset parameter or a custom property means.
func TestLoadFromMap_FreeFormYesStaysAString(t *testing.T) {
	s, err := LoadFromMap(map[string][]byte{
		"rulesets.yml": []byte("_version: 1\nrulesets:\n  - name: r\n    target: branch\n    enforcement: active\n" +
			"    rules:\n      - type: pull_request\n        parameters:\n          dismiss_stale_reviews_on_push: yes\n"),
		"custom-properties.yml": []byte("_version: 1\ncustom_properties:\n  flag: yes\n"),
	})
	if err != nil {
		t.Fatalf("free-form yes must load: %v", err)
	}
	if got := s.Rulesets.Rulesets[0].Rules[0].Parameters["dismiss_stale_reviews_on_push"]; got != "yes" {
		t.Errorf("ruleset parameter loaded as %#v, want \"yes\"", got)
	}
	if got := s.CustomProperties.Properties["flag"]; got != "yes" {
		t.Errorf("custom property loaded as %#v, want \"yes\"", got)
	}
}

// Three wrong booleans are three fixable problems, as unknown fields
// are.
func TestLoadFromMap_ReportsEachWrongBooleanSeparately(t *testing.T) {
	_, err := LoadFromMap(map[string][]byte{
		"repo.yml": []byte("_version: 1\nrepository:\n  has_issues: yes\n  has_wiki: off\n  private: maybe\n"),
	})
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	want := []string{
		"line 3: repository.has_issues: must be true or false",
		"line 4: repository.has_wiki: must be true or false",
		"line 5: repository.private: must be true or false",
	}
	if !slices.Equal(ve.Issues, want) {
		t.Fatalf("Issues = %#v, want %#v", ve.Issues, want)
	}
}
