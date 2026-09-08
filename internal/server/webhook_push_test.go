package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

func TestWebhookPush_DefaultBranchGate(t *testing.T) {
	stop := errors.New("stop after settings load")
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCalls  int
		wantRef    string
	}{
		{
			name:       "missing default branch fails safe",
			body:       `{"ref":"refs/heads/main","repository":{"owner":{"login":"o"},"name":"r"}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-default branch is skipped",
			body:       `{"ref":"refs/heads/feature","repository":{"owner":{"login":"o"},"name":"r","default_branch":"main"}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "default branch proceeds",
			body:       `{"ref":"refs/heads/main","repository":{"owner":{"login":"o"},"name":"r","default_branch":"main"}}`,
			wantStatus: http.StatusInternalServerError,
			wantCalls:  1,
			wantRef:    "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			gotRef := ""
			s := New(":0", Deps{
				Logger:               logger.Discard(),
				AllowUnauthenticated: true,
				Settings: func(_ context.Context, _ config.Repo, ref string) (*config.Resolution, error) {
					calls++
					gotRef = ref
					return nil, stop
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(tt.body))
			req.Header.Set("X-GitHub-Event", "push")
			rr := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if calls != tt.wantCalls {
				t.Fatalf("settings calls = %d, want %d", calls, tt.wantCalls)
			}
			if gotRef != tt.wantRef {
				t.Fatalf("settings ref = %q, want %q", gotRef, tt.wantRef)
			}
			if tt.wantCalls == 0 && !strings.Contains(rr.Body.String(), `"skipped"`) {
				t.Fatalf("skipped response missing: %s", rr.Body.String())
			}
		})
	}
}
