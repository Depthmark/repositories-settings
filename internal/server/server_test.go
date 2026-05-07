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
