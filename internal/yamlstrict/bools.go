package yamlstrict

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// boolWalk refuses every bool field whose value is not a YAML boolean.
//
// yaml.v3 keeps a YAML 1.1 rule for a Go bool target only: y, yes, on,
// n, no and off, bare or quoted, decode into it (decode.go, the
// reflect.Bool case). So `lock_branch: "yes"` loads as true, while the
// same word in a free-form field stays the string "yes". The walk reads
// the parsed document beside the destination type, so it sees what was
// written before yaml.v3 bends it, and it names the field.
type boolWalk struct {
	// seen bounds the walk: an anchored node is checked once per Go
	// type, however many aliases point at it, so alias chains and
	// self-referencing anchors cannot make it loop or blow up.
	seen   map[visit]bool
	issues []string
	lines  map[int]bool
}

type visit struct {
	node *yaml.Node
	typ  reflect.Type
}

var unmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()

// checkBools walks doc against the type of destination.
func checkBools(doc *yaml.Node, destination any) *boolWalk {
	w := &boolWalk{seen: map[visit]bool{}, lines: map[int]bool{}}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		w.walk(doc.Content[0], reflect.TypeOf(destination), "")
	}
	return w
}

func (w *boolWalk) walk(n *yaml.Node, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil || w.seen[visit{n, t}] {
		return
	}
	w.seen[visit{n, t}] = true
	// A type that decodes itself decides what its node means.
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		return
	}

	switch t.Kind() {
	case reflect.Bool:
		if !isBool(n) {
			w.issues = append(w.issues, fmt.Sprintf("line %d: %s: must be true or false", n.Line, path))
			w.lines[n.Line] = true
		}
	case reflect.Slice, reflect.Array:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range n.Content {
			w.walk(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Struct, reflect.Map:
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind == yaml.ScalarNode && key.ShortTag() == "!!merge" {
				w.merge(value, t, path)
				continue
			}
			if ft, ok := fieldType(t, key.Value); ok {
				w.walk(value, ft, join(path, key.Value))
			}
		}
	}
	// Anything else, `any` above all, is free-form: ruleset parameters
	// and custom properties keep the YAML value as written.
}

// merge walks the mappings a `<<` key merges in, as fields of the
// mapping that holds the key.
func (w *boolWalk) merge(value *yaml.Node, t reflect.Type, path string) {
	if value.Kind == yaml.SequenceNode {
		for _, m := range value.Content {
			w.walk(m, t, path)
		}
		return
	}
	w.walk(value, t, path)
}

// covers reports whether msg is yaml.v3's line-only refusal of a value
// the walk already named, such as "line 4: cannot unmarshal !!str
// `maybe` into bool", so that each field is reported once.
func (w *boolWalk) covers(msg string) bool {
	return w.lines[lineOf(msg)] &&
		strings.Contains(msg, ": cannot unmarshal ") &&
		strings.HasSuffix(msg, " into bool")
}

// isBool reports whether n is a YAML boolean: a scalar that yaml.v3,
// with no Go bool to bend it, decodes into a bool. That is the YAML 1.2
// core schema: true, True, TRUE and their false forms. A null passes
// too: it leaves the field unset, as it does every other field.
func isBool(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return false
	}
	switch v.(type) {
	case bool, nil:
		return true
	}
	return false
}

// fieldType returns the Go type yaml.v3 decodes key into within t, a
// struct or a map.
func fieldType(t reflect.Type, key string) (reflect.Type, bool) {
	if t.Kind() == reflect.Map {
		return t.Elem(), true
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == key {
			return f.Type, true
		}
	}
	return nil, false
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// lineOf reads N from a "line N: ..." message, so that a file's issues
// read top to bottom. A message without a line sorts last.
func lineOf(msg string) int {
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		if n, _, ok := strings.Cut(rest, ":"); ok {
			if line, err := strconv.Atoi(n); err == nil {
				return line
			}
		}
	}
	return math.MaxInt
}
