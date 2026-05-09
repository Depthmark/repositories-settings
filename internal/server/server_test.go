package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

func TestHealthAndReady(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard()})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/healthz status %d", rr.Code)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr2 := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("/readyz status %d", rr2.Code)
	}
}

func TestVerifyHMAC_GoodAndBad(t *testing.T) {
	secret := []byte("topsecret")
	body := []byte(`{"x":1}`)
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifyHMAC(secret, body, good) {
		t.Fatal("good signature rejected")
	}
	if verifyHMAC(secret, body, "sha256=deadbeef") {
		t.Fatal("bad signature accepted")
	}
	if verifyHMAC(secret, body, "") {
		t.Fatal("empty signature accepted")
	}
}

func TestValidateHandler_Happy(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard()})
	body := map[string]any{
		"files": []map[string]string{
			{"name": "teams.yml", "content": "_version: 1\nteams:\n  - slug: x\n    permission: push\n"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var resp validateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.Valid {
		t.Fatalf("expected valid, got %+v", resp)
	}
}

func TestValidateHandler_BadYAML(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard()})
	body := map[string]any{
		"files": []map[string]string{
			{"name": "teams.yml", "content": "_version: 2\nteams: []\n"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	var resp validateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Valid {
		t.Fatal("expected invalid")
	}
	if !strings.Contains(rr.Body.String(), "_version") {
		t.Fatalf("issues missing version error: %s", rr.Body.String())
	}
}

// /api/validate must warn (not fail) when the YAML is structurally
// valid but configures a section the operator has disabled.
func TestValidateHandler_DisabledResourcesSurfaceAsWarnings(t *testing.T) {
	disabled, _ := config.ParseDisabledResources("pages,secrets,deploy-keys")
	s := New(":0", Deps{Logger: logger.Discard(), DisabledResources: disabled})

	body := map[string]any{
		"files": []map[string]string{
			{"name": "teams.yml", "content": "_version: 1\nteams:\n  - slug: x\n    permission: push\n"},
			{"name": "pages.yml", "content": "_version: 1\npages:\n  build_type: workflow\n"},
			{"name": "secrets.yml", "content": "_version: 1\nrepository_secrets:\n  - name: FOO\n"},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var resp validateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Valid {
		t.Fatalf("expected Valid=true (disabled is a warning, not a failure), got %+v", resp)
	}

	// Top-level rollup includes only the keys that actually appeared
	// in submitted files (pages, secrets) — not deploy_keys, which
	// the operator disabled but no submitted file references.
	wantTop := map[string]bool{"pages": true, "secrets": true}
	if len(resp.Disabled) != len(wantTop) {
		t.Fatalf("top-level Disabled = %v, want %v", resp.Disabled, wantTop)
	}
	for _, k := range resp.Disabled {
		if !wantTop[k] {
			t.Errorf("unexpected key %q in top-level Disabled", k)
		}
	}

	// Per-file Disabled is set on the matching file outcomes only.
	byFile := map[string]validateFileOutcome{}
	for _, f := range resp.Files {
		byFile[f.File] = f
	}
	if got := byFile["pages.yml"].Disabled; len(got) != 1 || got[0] != "pages" {
		t.Errorf("pages.yml Disabled = %v, want [pages]", got)
	}
	if got := byFile["secrets.yml"].Disabled; len(got) != 1 || got[0] != "secrets" {
		t.Errorf("secrets.yml Disabled = %v, want [secrets]", got)
	}
	if got := byFile["teams.yml"].Disabled; len(got) != 0 {
		t.Errorf("teams.yml shouldn't carry Disabled, got %v", got)
	}
}

func TestReconcileHandler_Auth(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard(), APIToken: "topsecret"})
	req := httptest.NewRequest(http.MethodPost, "/api/reconcile",
		strings.NewReader(`{"owner":"o","repo":"r"}`))
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestWebhook_RejectsBadSig(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard(), WebhookSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/webhook", io.NopCloser(strings.NewReader("{}")))
	req.Header.Set("X-Hub-Signature-256", "sha256=00")
	req.Header.Set("X-GitHub-Event", "push")
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
