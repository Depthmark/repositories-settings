package applier

import (
	"encoding/json"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
)

// An optional field the operator did not mention must not appear in the
// desired document, and therefore must not produce a diff against
// whatever GitHub currently has. "Unmentioned" means "unmanaged".
func TestDesiredDocument_OmittedFieldIsUnmanaged(t *testing.T) {
	desired, err := diff.ToMaps([]config.Autolink{{
		KeyPrefix: "JIRA-", URLTemplate: "https://j/<num>",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := desired[0]["is_alphanumeric"]; ok {
		t.Fatalf("unmanaged field leaked into desired document: %#v", desired[0])
	}
	live := []map[string]any{{
		"_id": 1, "key_prefix": "JIRA-", "url_template": "https://j/<num>", "is_alphanumeric": true,
	}}
	ds := diff.NamedList("autolinks", live, desired, "key_prefix")
	if len(ds) != 1 || ds[0].Action != diff.Noop {
		t.Fatalf("expected noop for unmanaged field, got %+v", ds)
	}
}

// The counterpart: an explicit `false` must survive into the desired
// document and produce a real diff. This is the bug that made
// `is_alphanumeric: false` a silent no-op.
func TestDesiredDocument_ExplicitFalseIsHonoured(t *testing.T) {
	desired, err := diff.ToMaps([]config.Autolink{{
		KeyPrefix: "JIRA-", URLTemplate: "https://j/<num>", IsAlphanumeric: config.Ptr(false),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := desired[0]["is_alphanumeric"]; !ok || got != false {
		t.Fatalf("explicit false lost: %#v", desired[0])
	}
	live := []map[string]any{{
		"_id": 1, "key_prefix": "JIRA-", "url_template": "https://j/<num>", "is_alphanumeric": true,
	}}
	ds := diff.NamedList("autolinks", live, desired, "key_prefix")
	if len(ds) != 1 || ds[0].Action != diff.Update {
		t.Fatalf("expected update, got %+v", ds)
	}
}

// Omitting `active:` from a webhook must NOT disable a live, working
// webhook. Before the tri-state change this produced
// {active: true -> false} and switched the hook off.
func TestDesiredDocument_OmittedWebhookActiveDoesNotDisable(t *testing.T) {
	desired, err := diff.ToMaps([]config.Webhook{{
		URL: "https://h/x", Events: []string{"push"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	live := []map[string]any{{
		"_id": 9, "url": "https://h/x", "content_type": "json",
		"insecure_ssl": false, "active": true, "events": []any{"push"},
	}}
	for _, d := range diff.NamedList("webhooks", live, desired, "url") {
		for _, c := range d.Changes {
			if c.Path == "active" {
				t.Fatalf("unmanaged `active` produced a change: %+v", c)
			}
		}
	}
}

// Fields that exist only in the YAML contract (a pointer to a secret
// held elsewhere, the deploy-key material itself) must never reach the
// diff document — GitHub does not report them, so they would diff
// forever.
func TestDesiredDocument_YAMLOnlyFieldsAreNotDiffed(t *testing.T) {
	hooks, err := diff.ToMaps([]config.Webhook{{URL: "https://h/x", SecretRef: "hook-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hooks[0]["secret_ref"]; ok {
		t.Errorf("secret_ref leaked into the diff document: %#v", hooks[0])
	}

	keys, err := diff.ToMaps([]config.DeployKey{{Title: "ci", Key: "ssh-ed25519 AAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"key", "key_ref"} {
		if _, ok := keys[0][banned]; ok {
			t.Errorf("%s leaked into the diff document: %#v", banned, keys[0])
		}
	}
}

// A JSON round trip of the whole schema must never emit a null for an
// optional scalar: null is what GitHub rejects with a 422.
func TestDesiredDocument_NoNullScalars(t *testing.T) {
	for name, v := range map[string]any{
		"webhook":   config.Webhook{URL: "https://h/x"},
		"autolink":  config.Autolink{KeyPrefix: "J-", URLTemplate: "https://j/<num>"},
		"deploykey": config.DeployKey{Title: "ci"},
		"checks":    config.RequiredStatusChecks{},
	} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for k, val := range m {
			if val == nil {
				t.Errorf("%s: field %q marshalled to null", name, k)
			}
		}
	}
}
