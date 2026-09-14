package config

import (
	"strings"
	"testing"
)

// A misspelled field is the single most common config-as-code mistake.
// It must fail loudly and name the field, not parse into silence.
func TestLoadFromMap_RejectsUnknownField(t *testing.T) {
	_, err := LoadFromMap(map[string][]byte{
		"repo.yml": []byte("_version: 1\nrepository:\n  has_issue: true\n"),
	})
	if err == nil {
		t.Fatal("expected an error for a misspelled field")
	}
	if !strings.Contains(err.Error(), "has_issue") {
		t.Errorf("error must name the offending field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error must say what kind of problem it is, got: %v", err)
	}
}

// Every unknown field gets its own issue: a file with three typos is
// three fixable problems, not one blob.
func TestLoadFromMap_ReportsEachUnknownFieldSeparately(t *testing.T) {
	_, err := LoadFromMap(map[string][]byte{
		"repo.yml": []byte("_version: 1\nrepository:\n  has_issue: true\n  has_wikis: true\n  privat: true\n"),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(ve.Issues) != 3 {
		t.Fatalf("expected one issue per unknown field, got %d: %v", len(ve.Issues), ve.Issues)
	}
}

func TestLoadFromMap_RejectsMultipleDocuments(t *testing.T) {
	_, err := LoadFromMap(map[string][]byte{
		"repo.yml": []byte("_version: 1\nrepository:\n  has_issues: true\n---\n_version: 1\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected a multi-document rejection, got: %v", err)
	}
}

// An empty file means "unmanaged", not "malformed".
func TestLoadFromMap_EmptyFileIsUnmanaged(t *testing.T) {
	s, err := LoadFromMap(map[string][]byte{"repo.yml": []byte("")})
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if s.Repo != nil || s.Topics != nil {
		t.Fatalf("empty file should leave the section unmanaged, got %+v", s)
	}
}

func TestLoadFromMap_UnknownFileNames(t *testing.T) {
	_, err := LoadFromMap(map[string][]byte{"reposettings.yml": []byte("_version: 1\n")})
	if err == nil || !strings.Contains(err.Error(), "unknown settings file") {
		t.Fatalf("expected unknown-file rejection, got: %v", err)
	}
}

// Fields the service accepts but does not apply must be called out, not
// silently ignored — silence is what made them look implemented.
func TestValidate_UnsupportedFieldsAreReported(t *testing.T) {
	cases := map[string]struct{ file, yaml, want string }{
		"webhook secret_ref": {
			"webhooks.yml",
			"_version: 1\nwebhooks:\n  - url: https://h/x\n    events: [push]\n    secret_ref: my-secret\n",
			"secret_ref",
		},
		"environment variables": {
			"environments.yml",
			"_version: 1\nenvironments:\n  - name: prod\n    variables:\n      - name: A\n        value: b\n",
			"variables",
		},
		"environment secrets": {
			"environments.yml",
			"_version: 1\nenvironments:\n  - name: prod\n    secrets:\n      - name: TOKEN\n",
			"secrets",
		},
		"named deployment branch policies": {
			"environments.yml",
			"_version: 1\nenvironments:\n  - name: prod\n    deployment_branch_policy:\n      custom_branch_policies: true\n      branch_policies:\n        - name: release/*\n",
			"branch_policies",
		},
		"deploy key reference": {
			"deploy-keys.yml",
			"_version: 1\ndeploy_keys:\n  - title: ci\n    key_ref: vault://x\n",
			"key_ref",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFromMap(map[string][]byte{tc.file: []byte(tc.yaml)})
			if err == nil {
				t.Fatal("expected the unsupported field to be reported")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "not supported yet") {
				t.Fatalf("expected a 'not supported yet' issue naming %q, got: %v", tc.want, err)
			}
		})
	}
}

func asValidationError(err error, target **ValidationError) bool {
	type unwrapper interface{ Unwrap() []error }
	if ve, ok := err.(*ValidationError); ok {
		*target = ve
		return true
	}
	if j, ok := err.(unwrapper); ok {
		for _, leaf := range j.Unwrap() {
			if ve, ok := leaf.(*ValidationError); ok {
				*target = ve
				return true
			}
		}
	}
	return false
}
