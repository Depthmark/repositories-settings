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
	// A schema failure is unprocessable content, not a successful
	// validation of an invalid file: the workflow branches on this.
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body.String())
	}
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

func TestPrivilegedIngress_FailsClosedWithoutCredentials(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "reconcile", method: http.MethodPost, path: "/api/reconcile", body: `{}`},
		{name: "check", method: http.MethodPost, path: "/api/check", body: `{}`},
		{name: "webhook", method: http.MethodPost, path: "/webhook", body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(":0", Deps{Logger: logger.Discard()})
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAuthorize_RequiresExactBearerOrExplicitDevOptIn(t *testing.T) {
	allowed := func(d Deps, bearer string) bool {
		req := httptest.NewRequest(http.MethodPost, "/api/reconcile", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		_, ok := authorize(req, d)
		return ok
	}

	if allowed(Deps{Logger: logger.Discard()}, "") {
		t.Fatal("empty API token authorized without explicit dev opt-in")
	}
	if !allowed(Deps{Logger: logger.Discard(), AllowUnauthenticated: true}, "") {
		t.Fatal("explicit dev opt-in did not authorize request")
	}

	withToken := Deps{Logger: logger.Discard(), APIToken: "topsecret"}
	if !allowed(withToken, "topsecret") {
		t.Fatal("exact bearer token rejected")
	}
	if allowed(withToken, "topsecret-extra") {
		t.Fatal("non-exact bearer token authorized")
	}
	if allowed(withToken, "") {
		t.Fatal("missing bearer authorized against a configured token")
	}
	// A configured token is not a fallback for a dev opt-in: the opt-in
	// only covers the case where no credential is configured at all.
	if allowed(Deps{Logger: logger.Discard(), APIToken: "topsecret", AllowUnauthenticated: true}, "wrong") {
		t.Fatal("dev opt-in let a wrong token through")
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

// A caller-supplied trace ID is echoed into a response header and into
// every log line for the request. Only a bounded, plain-token shape is
// honoured, so neither surface can be forged.
func TestTraceMiddleware_RejectsUnsafeRequestIDs(t *testing.T) {
	s := New(":0", Deps{Logger: logger.Discard()})

	t.Run("a plain token is preserved", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", "abc-123_XYZ")
		rr := httptest.NewRecorder()
		s.httpSrv.Handler.ServeHTTP(rr, req)
		if got := rr.Header().Get("X-Request-ID"); got != "abc-123_XYZ" {
			t.Fatalf("X-Request-ID = %q, want it preserved", got)
		}
	})

	for _, bad := range []string{
		"has space",
		"line\nbreak",
		"semi;colon",
		strings.Repeat("a", 65),
	} {
		t.Run("rejected: "+bad[:min(len(bad), 12)], func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set("X-Request-ID", bad)
			rr := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(rr, req)
			got := rr.Header().Get("X-Request-ID")
			if got == bad {
				t.Fatalf("unsafe X-Request-ID %q was echoed back", bad)
			}
			if got == "" {
				t.Fatal("a generated trace ID should have replaced it")
			}
		})
	}
}
