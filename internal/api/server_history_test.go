package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/history"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func serveHistoryTest(server *Server, path, authorization string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	return rec.Code
}

func TestHistoryRoutesAbsentWithoutHistory(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/usage", "/history"} {
		if code := serveHistoryTest(server, path, "Bearer test-key"); code != http.StatusNotFound {
			t.Fatalf("%s without history: status %d, want 404", path, code)
		}
	}
}

func TestHistoryKeyAndClientKeysAreSeparate(t *testing.T) {
	server := newTestServer(t)
	cfg := proxyconfig.HistoryConfig{Enabled: true, DBPath: filepath.Join(t.TempDir(), "history.db"), Key: "history-key"}
	hist, errStart := history.Start(context.Background(), cfg, auth.NewManager(nil, nil, nil))
	if errStart != nil {
		t.Fatalf("history.Start: %v", errStart)
	}
	t.Cleanup(func() {
		if errClose := hist.Close(); errClose != nil {
			t.Errorf("close history: %v", errClose)
		}
	})
	server.AttachRoutes(hist.Register)

	for _, path := range []string{"/usage", "/history"} {
		if code := serveHistoryTest(server, path, "Bearer test-key"); code != http.StatusUnauthorized {
			t.Fatalf("%s with the client key: status %d, want 401", path, code)
		}
		if code := serveHistoryTest(server, path, "Bearer history-key"); code != http.StatusOK {
			t.Fatalf("%s with the history key: status %d, want 200", path, code)
		}
	}
	if code := serveHistoryTest(server, "/v1/models", "Bearer history-key"); code != http.StatusUnauthorized {
		t.Fatalf("/v1/models with the history key: status %d, want 401", code)
	}
	if code := serveHistoryTest(server, "/v1/models", "Bearer test-key"); code != http.StatusOK {
		t.Fatalf("/v1/models with the client key: status %d, want 200", code)
	}
}
