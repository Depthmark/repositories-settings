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
// Modelled on the package of the same name in github-sts.
package yamlstrict

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrEmpty reports a document with no content. Callers decide what an
// empty file means; for settings files it means "unmanaged", and the
// caller must NOT apply the zero-valued envelope — doing so would turn
// an empty collaborators.yml into "remove every collaborator".
var ErrEmpty = errors.New("empty YAML document")

// Decode decodes exactly one YAML document into destination and rejects
// any field not represented by a yaml tag on it. yaml.v3 additionally
// rejects duplicate mapping keys while decoding.
//
// Returns ErrEmpty when the document holds nothing.
func Decode(data []byte, destination any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(destination); err != nil {
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
	return nil
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
