package yamlstrict

import (
	"errors"
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
