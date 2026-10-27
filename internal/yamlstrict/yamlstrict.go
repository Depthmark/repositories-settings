// Package yamlstrict decodes configuration YAML with a single-document,
// known-field contract.
//
// The default yaml.Unmarshal silently drops fields it does not
// recognise. For configuration-as-code that is the worst possible
// default: `has_issue: true` (singular, a typo) parses cleanly, sets
// nothing, and reports nothing, so the operator sees a green check on a
// change that does nothing. KnownFields(true) turns that into an error
// naming the offending field.
//
// Booleans get the same treatment: yaml.v3 decodes `yes`, `on` and
// their opposites into a Go bool, so `lock_branch: yes` would load as
// true. Decode refuses any bool field whose value is not true or false,
// and names the field.
//
// Modelled on the package of the same name in github-sts.
package yamlstrict

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrEmpty reports a document with no content. Callers decide what an
// empty file means; for settings files it means "unmanaged", and the
// caller must NOT apply the zero-valued envelope — doing so would turn
// an empty collaborators.yml into "remove every collaborator".
var ErrEmpty = errors.New("empty YAML document")

// Decode decodes exactly one YAML document into destination and rejects
// any field not represented by a yaml tag on it, and any bool field
// whose value is not a YAML boolean. yaml.v3 additionally rejects
// duplicate mapping keys while decoding.
//
// Returns ErrEmpty when the document holds nothing.
func Decode(data []byte, destination any) error {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return ErrEmpty
		}
		return err
	}

	var extra yaml.Node
	err := dec.Decode(&extra)
	if err == nil {
		return fmt.Errorf("multiple YAML documents are not allowed in one settings file")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}

	// The walk runs on the parsed node, before yaml.v3 turns `yes`
	// into true. The decode still runs after it, so a file with both
	// a wrong boolean and a typo reports both.
	bools := checkBools(&doc, destination)
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	return withBoolIssues(strict.Decode(destination), bools)
}

// withBoolIssues joins the walk's issues to what the decode reported, in
// line order, as one TypeError so that Issues splits them.
func withBoolIssues(err error, bools *boolWalk) error {
	if len(bools.issues) == 0 {
		return err
	}
	out := bools.issues
	var te *yaml.TypeError
	switch {
	case errors.As(err, &te):
		for _, msg := range te.Errors {
			if !bools.covers(msg) {
				out = append(out, msg)
			}
		}
	case err != nil:
		// The decode stopped, for example on an explicit `!!bool yes`;
		// the fields the walk named are still worth reporting.
		out = append(out, err.Error())
	}
	slices.SortStableFunc(out, func(a, b string) int { return cmp.Compare(lineOf(a), lineOf(b)) })
	return &yaml.TypeError{Errors: out}
}

// Issues splits a decode error into one message per problem.
//
// yaml.v3 reports every unknown field of a document in a single
// multi-line TypeError. Presenting that as one blob in a PR comment
// makes a five-typo file look like one inscrutable failure, so it is
// unpacked into a message per field here.
func Issues(err error) []string {
	if err == nil {
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		out := make([]string, 0, len(te.Errors))
		for _, e := range te.Errors {
			out = append(out, humanize(e))
		}
		return out
	}
	return []string{humanize(err.Error())}
}

// humanize rewrites yaml.v3's phrasing into something that tells the
// reader what to do about it.
func humanize(msg string) string {
	msg = strings.TrimSpace(msg)
	// "line 4: field has_issue not found in type config.RepoConfig"
	if i := strings.Index(msg, "field "); i >= 0 {
		if j := strings.Index(msg[i:], " not found in type"); j >= 0 {
			field := msg[i+len("field ") : i+j]
			return fmt.Sprintf("%s: unknown field %q — check the spelling, or remove it if the setting is not supported",
				linePrefix(msg), field)
		}
	}
	return msg
}

// linePrefix keeps yaml.v3's "line N" locator when it has one so the
// reader can jump straight to it.
func linePrefix(msg string) string {
	if strings.HasPrefix(msg, "line ") {
		if i := strings.Index(msg, ":"); i > 0 {
			return msg[:i]
		}
	}
	return "yaml"
}
