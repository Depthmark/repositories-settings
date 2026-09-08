// Package diff computes per-resource configuration drift.
//
// Three flavors:
//   - Fields:        deep object compare. Intentionally one-sided — keys
//     present in current but not in desired are NOT reported
//     so that fields the operator is not managing are not
//     clobbered.
//   - NamedList:     create / update / delete / noop diffs for arrays
//     keyed by a named field (rulesets by name, teams by
//     slug, ...).
//   - SingleResource: same logic for singletons (repo settings, actions
//     config, ...).
//
// Mirrors src/core/diff-engine.ts.
package diff

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"
)

type Action string

const (
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
	Noop   Action = "noop"
)

type FieldChange struct {
	Path string `json:"path"`
	From any    `json:"from,omitempty"`
	To   any    `json:"to,omitempty"`
}

type Diff struct {
	Resource string        `json:"resource"`
	Action   Action        `json:"action"`
	Current  any           `json:"current"`
	Desired  any           `json:"desired"`
	Changes  []FieldChange `json:"changes"`
}

// Fields deep-compares two maps and returns field-level changes. Keys in
// current but not in desired are ignored — managed only what is set.
func Fields(current, desired map[string]any, prefix string) []FieldChange {
	keys := unionKeys(current, desired)
	out := make([]FieldChange, 0, len(keys))
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		curV, curOK := current[k]
		desV, desOK := desired[k]

		if !desOK {
			// Unmanaged: don't report.
			continue
		}
		if !curOK {
			out = append(out, FieldChange{Path: path, From: nil, To: desV})
			continue
		}

		curMap, curIsMap := curV.(map[string]any)
		desMap, desIsMap := desV.(map[string]any)
		if curIsMap && desIsMap {
			out = append(out, Fields(curMap, desMap, path)...)
			continue
		}

		if isSlice(curV) && isSlice(desV) {
			if !slicesEqualByJSON(curV, desV) {
				out = append(out, FieldChange{Path: path, From: curV, To: desV})
			}
			continue
		}

		if !valuesEqual(curV, desV) {
			out = append(out, FieldChange{Path: path, From: curV, To: desV})
		}
	}
	return out
}

// NamedList diffs two slices of objects keyed by the value at keyField.
// Items must be representable as map[string]any (use ToMap to convert).
func NamedList(resource string, current, desired []map[string]any, keyField string) []Diff {
	curIdx := indexBy(current, keyField)
	desIdx := indexBy(desired, keyField)

	results := make([]Diff, 0, len(curIdx)+len(desIdx))

	// Creates (in desired, not in current).
	for _, key := range sortedKeys(desIdx) {
		if _, exists := curIdx[key]; exists {
			continue
		}
		desItem := desIdx[key]
		changes := make([]FieldChange, 0, len(desItem))
		for _, fk := range sortedKeys(desItem) {
			changes = append(changes, FieldChange{Path: fk, From: nil, To: desItem[fk]})
		}
		results = append(results, Diff{
			Resource: resource + "." + key,
			Action:   Create,
			Current:  nil,
			Desired:  desItem,
			Changes:  changes,
		})
	}

	// Updates (in both).
	for _, key := range sortedKeys(desIdx) {
		curItem, exists := curIdx[key]
		if !exists {
			continue
		}
		desItem := desIdx[key]
		changes := Fields(curItem, desItem, "")
		action := Noop
		if len(changes) > 0 {
			action = Update
		}
		results = append(results, Diff{
			Resource: resource + "." + key,
			Action:   action,
			Current:  curItem,
			Desired:  desItem,
			Changes:  changes,
		})
	}

	// Deletes (in current, not in desired).
	for _, key := range sortedKeys(curIdx) {
		if _, exists := desIdx[key]; exists {
			continue
		}
		results = append(results, Diff{
			Resource: resource + "." + key,
			Action:   Delete,
			Current:  curIdx[key],
			Desired:  nil,
			Changes:  nil,
		})
	}

	return results
}

// SingleResource diffs a single object — current/desired may be nil.
func SingleResource(resource string, current, desired map[string]any) Diff {
	switch {
	case current == nil && desired == nil:
		return Diff{Resource: resource, Action: Noop}
	case current == nil:
		changes := make([]FieldChange, 0, len(desired))
		for _, k := range sortedKeys(desired) {
			changes = append(changes, FieldChange{Path: k, From: nil, To: desired[k]})
		}
		return Diff{Resource: resource, Action: Create, Desired: desired, Changes: changes}
	case desired == nil:
		return Diff{Resource: resource, Action: Delete, Current: current}
	default:
		changes := Fields(current, desired, "")
		action := Noop
		if len(changes) > 0 {
			action = Update
		}
		return Diff{Resource: resource, Action: action, Current: current, Desired: desired, Changes: changes}
	}
}

// ToMap reflects a Go struct or pointer to a map[string]any using JSON
// tags. Used by appliers to feed typed configs into the generic differ.
func ToMap(v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding for diff: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decoding for diff: %w", err)
	}
	return m, nil
}

// ToMaps converts a slice of typed structs into []map[string]any.
func ToMaps[T any](items []T) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		m, err := ToMap(it)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// internals

func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func indexBy(items []map[string]any, key string) map[string]map[string]any {
	out := make(map[string]map[string]any, len(items))
	for _, it := range items {
		v, ok := it[key]
		if !ok {
			continue
		}
		out[fmt.Sprint(v)] = it
	}
	return out
}

func isSlice(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}

func slicesEqualByJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// valuesEqual compares one live value against one desired value.
//
// The only coercion is numeric: a JSON round-trip widens every number to
// float64, so live 1.0 and desired 1 describe the same setting. Nothing
// else is coerced. Comparing the rendered strings — the previous
// behaviour — made "true" equal true and "1" equal 1, which silently
// swallowed exactly the typed drift this package exists to report.
func valuesEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, aNum := numeric(a)
	br, bNum := numeric(b)
	if aNum || bNum {
		// SetFloat64 returns nil for NaN and infinities. Those values cannot
		// occur in JSON configuration, and treating them as unequal avoids
		// manufacturing equality for an invalid numeric value.
		return aNum && bNum && ar != nil && br != nil && ar.Cmp(br) == 0
	}
	return reflect.DeepEqual(a, b)
}

// numeric converts any Go numeric kind to its exact rational value.
// Using float64 as the common type would make adjacent integers above
// 2^53 compare equal. Strings and bools are deliberately excluded: they
// are the coercions that hid drift.
func numeric(v any) (*big.Rat, bool) {
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(rv.Uint())), true
	case reflect.Float32, reflect.Float64:
		return new(big.Rat).SetFloat64(rv.Float()), true
	default:
		return nil, false
	}
}
