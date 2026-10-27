package yamlstrict

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type doc struct {
	Version int    `yaml:"_version"`
	Name    string `yaml:"name"`
	Nested  struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"nested"`
}

func TestDecode_AcceptsKnownFields(t *testing.T) {
	var d doc
	if err := Decode([]byte("_version: 1\nname: x\nnested:\n  enabled: true\n"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Name != "x" || !d.Nested.Enabled {
		t.Fatalf("decoded wrong: %+v", d)
	}
}

// The whole reason this package exists: a typo must not parse into
// silence.
func TestDecode_RejectsUnknownField(t *testing.T) {
	var d doc
	err := Decode([]byte("_version: 1\nnam: x\n"), &d)
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
	issues := Issues(err)
	if len(issues) != 1 || !strings.Contains(issues[0], `unknown field "nam"`) {
		t.Fatalf("expected one issue naming the field, got %#v", issues)
	}
	if !strings.Contains(issues[0], "line 2") {
		t.Errorf("issue should locate the problem, got %q", issues[0])
	}
}

// One message per typo: a file with three mistakes is three fixable
// problems, not one blob to squint at.
func TestIssues_SplitsMultipleUnknownFields(t *testing.T) {
	var d doc
	err := Decode([]byte("_version: 1\na: 1\nb: 2\nc: 3\n"), &d)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := Issues(err); len(got) != 3 {
		t.Fatalf("expected 3 issues, got %d: %#v", len(got), got)
	}
}

func TestDecode_RejectsMultipleDocuments(t *testing.T) {
	var d doc
	err := Decode([]byte("_version: 1\n---\n_version: 1\n"), &d)
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected a multi-document rejection, got %v", err)
	}
}

// Emptiness is a distinct outcome, not an error: callers decide what it
// means, and for settings files it means "unmanaged".
func TestDecode_EmptyDocumentIsItsOwnSignal(t *testing.T) {
	for _, in := range []string{"", "   \n", "# just a comment\n"} {
		var d doc
		if err := Decode([]byte(in), &d); !errors.Is(err, ErrEmpty) {
			t.Errorf("Decode(%q) = %v, want ErrEmpty", in, err)
		}
	}
}

// yaml.v3 rejects duplicate keys while decoding into a Go value, which
// is the other silent-config-error class this package guards.
func TestDecode_RejectsDuplicateKeys(t *testing.T) {
	var d doc
	if err := Decode([]byte("_version: 1\nname: a\nname: b\n"), &d); err == nil {
		t.Fatal("expected duplicate keys to be rejected")
	}
}

func TestIssues_NilErrorHasNoIssues(t *testing.T) {
	if got := Issues(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

type flags struct {
	Enabled *bool          `yaml:"enabled"`
	Plain   bool           `yaml:"plain"`
	List    []bool         `yaml:"list"`
	Free    any            `yaml:"free"`
	Params  map[string]any `yaml:"params"`
}

// yaml.v3 decodes yes, on and their opposites into a Go bool, quoted
// or not, and refuses "true" and 1 by line only. Each is refused here,
// with the field named.
func TestDecode_RefusesNonBooleans(t *testing.T) {
	for _, value := range []string{"yes", "on", "Y", "off", `"yes"`, `"true"`, "1", "maybe", "[true]", "{a: 1}"} {
		for _, field := range []string{"enabled", "plain"} {
			t.Run(field+"="+value, func(t *testing.T) {
				var f flags
				err := Decode([]byte(field+": "+value+"\n"), &f)
				want := "line 1: " + field + ": must be true or false"
				if got := Issues(err); len(got) != 1 || got[0] != want {
					t.Fatalf("Issues = %#v, want [%q]", got, want)
				}
			})
		}
	}
}

// The YAML 1.2 core schema: what yaml.v3 tags !!bool loads. A null
// leaves the field unset, as for every other field.
func TestDecode_AcceptsYAMLBooleans(t *testing.T) {
	cases := map[string]*bool{
		"true": ptr(true), "True": ptr(true), "TRUE": ptr(true),
		"false": ptr(false), "False": ptr(false), "FALSE": ptr(false),
		"null": nil, "": nil,
	}
	for value, want := range cases {
		var f flags
		if err := Decode([]byte("enabled: "+value+"\n"), &f); err != nil {
			t.Fatalf("enabled: %s: %v", value, err)
		}
		if (f.Enabled == nil) != (want == nil) || (want != nil && *f.Enabled != *want) {
			t.Errorf("enabled: %s loaded as %v, want %v", value, f.Enabled, want)
		}
	}
}

// Free-form values have no Go bool to bend them: yes stays the string
// the operator wrote.
func TestDecode_FreeFormValuesKeepYes(t *testing.T) {
	var f flags
	if err := Decode([]byte("free: yes\nparams:\n  push: yes\n"), &f); err != nil {
		t.Fatal(err)
	}
	if f.Free != "yes" || f.Params["push"] != "yes" {
		t.Fatalf("free = %#v, params = %#v, want the string \"yes\"", f.Free, f.Params)
	}
}

// Every problem in the file at once, in line order: wrong booleans and
// unknown fields alike, one issue each.
func TestIssues_OneIssuePerWrongBooleanAmongUnknownFields(t *testing.T) {
	var d doc
	err := Decode([]byte("_version: 1\nnested:\n  enabled: yes\nnam: x\n"), &d)
	got := Issues(err)
	if len(got) != 2 || got[0] != "line 3: nested.enabled: must be true or false" || !strings.Contains(got[1], `line 4: unknown field "nam"`) {
		t.Fatalf("Issues = %#v", got)
	}

	var f flags
	err = Decode([]byte("enabled: yes\nplain: maybe\nlist: [true, on, 1]\n"), &f)
	want := []string{
		"line 1: enabled: must be true or false",
		"line 2: plain: must be true or false",
		"line 3: list[1]: must be true or false",
		"line 3: list[2]: must be true or false",
	}
	if got := Issues(err); !slices.Equal(got, want) {
		t.Fatalf("Issues = %#v, want %#v", got, want)
	}
}

// An anchor written in a free-form field is still checked where an
// alias puts it in a bool field, once however often it is used.
func TestDecode_FollowsAliasesAndMergeKeys(t *testing.T) {
	var f flags
	err := Decode([]byte("free: &a yes\nlist: [*a, *a, *a]\n"), &f)
	if got := Issues(err); len(got) != 1 || got[0] != "line 1: list[0]: must be true or false" {
		t.Fatalf("alias: Issues = %#v", got)
	}

	var d doc
	err = Decode([]byte("_version: 1\nnested:\n  <<: {enabled: on}\n"), &d)
	if got := Issues(err); len(got) != 1 || got[0] != "line 3: nested.enabled: must be true or false" {
		t.Fatalf("merge key: Issues = %#v", got)
	}
}

// yaml.v3 stops on an explicit tag it cannot honour, without a line;
// the field is still named first.
func TestDecode_ExplicitBoolTagStillNamesTheField(t *testing.T) {
	var f flags
	got := Issues(Decode([]byte("enabled: !!bool yes\n"), &f))
	if len(got) != 2 || got[0] != "line 1: enabled: must be true or false" || !strings.Contains(got[1], "cannot decode") {
		t.Fatalf("Issues = %#v", got)
	}
}

// An anchor inside itself must not make the walk loop.
func TestDecode_SelfReferencingAnchorEnds(t *testing.T) {
	var f flags
	if err := Decode([]byte("list: &a [*a]\n"), &f); err == nil {
		t.Fatal("expected an error")
	}
}

func ptr[T any](v T) *T { return &v }
