package history

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const testKey = "history-test-key"

const bothWindows = `{
	"five_hour": {"utilization": 99, "resets_at": "2026-10-28T16:00:00+00:00"},
	"seven_day": {"utilization": 98, "resets_at": "2026-11-03T22:00:00+00:00"},
	"limits": [
		{"kind": "session", "percent": 56, "resets_at": "2026-10-28T16:00:00.289020+00:00"},
		{"kind": "weekly_all", "percent": 14.5, "resets_at": "2026-11-03T22:00:00.289040+00:00"},
		{"kind": "weekly_scoped", "percent": 3, "resets_at": "2026-11-03T22:00:00+00:00"}
	]
}`

// fakeAccounts serves canned usage responses per auth ID and records the requests.
type fakeAccounts struct {
	auths     []*coreauth.Auth
	responses map[string]func(ctx context.Context) (*http.Response, error)

	mu       sync.Mutex
	requests []*http.Request
	authIDs  []string
}

func (f *fakeAccounts) List() []*coreauth.Auth { return f.auths }

func (f *fakeAccounts) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.authIDs = append(f.authIDs, auth.ID)
	f.mu.Unlock()
	return f.responses[auth.ID](ctx)
}

func respond(status int, body string) func(context.Context) (*http.Response, error) {
	return func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func claudeAuth(id, uuid, email string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "token-" + id, "email": email, "account_uuid": uuid},
	}
}

// newTestService opens a store without starting the poller, so tests call poll themselves.
func newTestService(t *testing.T, accounts Accounts) *Service {
	t.Helper()
	st, errOpen := openStore(filepath.Join(t.TempDir(), "history.db"))
	if errOpen != nil {
		t.Fatalf("openStore: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := st.close(); errClose != nil {
			t.Errorf("close store: %v", errClose)
		}
	})
	now := time.Unix(1_800_000_000, 0)
	return &Service{key: testKey, accounts: accounts, store: st, now: func() time.Time { return now }}
}

func serve(t *testing.T, s *Service, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	s.Register(engine)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestStartDisabledOpensNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	accounts := &fakeAccounts{auths: []*coreauth.Auth{claudeAuth("a", "uuid-a", "a@example.com")}}
	for _, cfg := range []config.HistoryConfig{
		{},
		{Enabled: false, DBPath: dbPath, Key: testKey},
	} {
		s, errStart := Start(context.Background(), cfg, accounts)
		if errStart != nil || s != nil {
			t.Fatalf("Start(%+v) = %v, %v; want nil, nil", cfg, s, errStart)
		}
	}
	if _, errStat := os.Stat(dbPath); !os.IsNotExist(errStat) {
		t.Fatalf("database was created: %v", errStat)
	}
	if len(accounts.requests) != 0 {
		t.Fatalf("disabled history polled %d times", len(accounts.requests))
	}
}

func TestStartRejectsBadConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	tests := []struct {
		name string
		cfg  config.HistoryConfig
		want string
	}{
		{"empty key", config.HistoryConfig{Enabled: true, DBPath: dbPath}, "key is empty"},
		{"relative db-path", config.HistoryConfig{Enabled: true, DBPath: "~/history.db", Key: testKey}, "absolute path"},
		{"negative interval", config.HistoryConfig{Enabled: true, DBPath: dbPath, Key: testKey, Interval: -time.Minute}, "interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, errStart := Start(context.Background(), tt.cfg, &fakeAccounts{})
			if errStart == nil || s != nil {
				t.Fatalf("Start = %v, %v; want an error", s, errStart)
			}
			if !strings.Contains(errStart.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", errStart, tt.want)
			}
		})
	}
	if _, errStat := os.Stat(dbPath); !os.IsNotExist(errStat) {
		t.Fatalf("database was created: %v", errStat)
	}
}

func TestParseUsageReadsOnlyLimits(t *testing.T) {
	readings, errParse := parseUsage([]byte(bothWindows))
	if errParse != nil {
		t.Fatalf("parseUsage: %v", errParse)
	}
	if len(readings) != 2 {
		t.Fatalf("got %d readings, want one per window: %+v", len(readings), readings)
	}
	session, weekly := readings[0], readings[1]
	if session.window != "5h" || session.utilization != 56 {
		t.Fatalf("session = %+v, want 5h at 56", session)
	}
	if weekly.window != "weekly" || weekly.utilization != 14.5 {
		t.Fatalf("weekly = %+v, want weekly at 14.5", weekly)
	}
	wantReset := time.Date(2026, 10, 28, 16, 0, 0, 289020000, time.UTC)
	if session.resetsAt == nil || !session.resetsAt.Equal(wantReset) {
		t.Fatalf("session reset = %v, want %v", session.resetsAt, wantReset)
	}
}

func TestParseUsageNullResetAndMissingWindow(t *testing.T) {
	readings, errParse := parseUsage([]byte(`{
		"five_hour": {"utilization": 40, "resets_at": "2026-10-28T16:00:00+00:00"},
		"limits": [
			{"kind": "session", "percent": 7, "resets_at": null},
			{"kind": "weekly_scoped", "percent": 3, "resets_at": null}
		]
	}`))
	if errParse != nil {
		t.Fatalf("parseUsage: %v", errParse)
	}
	if len(readings) != 1 || readings[0].window != "5h" || readings[0].utilization != 7 || readings[0].resetsAt != nil {
		t.Fatalf("readings = %+v, want only 5h at 7 with a null reset", readings)
	}

	readings, errParse = parseUsage([]byte(`{"limits": [{"kind": "weekly_all", "percent": null, "resets_at": null}]}`))
	if errParse != nil || len(readings) != 0 {
		t.Fatalf("a null percent gave %+v, %v; want no readings", readings, errParse)
	}
}

func TestPollStoresSamplesAndHistoryReturnsThem(t *testing.T) {
	disabled := claudeAuth("off", "uuid-off", "off@example.com")
	disabled.Disabled = true
	codex := &coreauth.Auth{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "x"}}
	accounts := &fakeAccounts{
		auths: []*coreauth.Auth{
			claudeAuth("b", "uuid-b", "b@example.com"),
			claudeAuth("a", "uuid-a", "a@example.com"),
			claudeAuth("c", "uuid-c", "c@example.com"),
			disabled,
			codex,
		},
		responses: map[string]func(context.Context) (*http.Response, error){
			"a": respond(http.StatusOK, bothWindows),
			"b": respond(http.StatusOK, `{"limits": [{"kind": "session", "percent": 20, "resets_at": null}]}`),
			"c": respond(http.StatusTooManyRequests, `{"error": "slow down"}`),
		},
	}
	s := newTestService(t, accounts)
	s.poll(context.Background())

	if strings.Join(accounts.authIDs, ",") != "a,b,c" {
		t.Fatalf("polled %v, want the enabled Claude accounts a,b,c", accounts.authIDs)
	}
	for _, req := range accounts.requests {
		if req.Method != http.MethodGet || req.URL.String() != usageURL {
			t.Fatalf("request = %s %s, want GET %s", req.Method, req.URL, usageURL)
		}
		if got := req.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Fatalf("anthropic-beta = %q", got)
		}
	}

	var nullResets int
	if errQuery := s.store.db.QueryRow(`SELECT COUNT(*) FROM usage_samples WHERE resets_at IS NULL`).Scan(&nullResets); errQuery != nil {
		t.Fatalf("count null resets: %v", errQuery)
	}
	if nullResets != 1 {
		t.Fatalf("%d samples with a null reset, want 1", nullResets)
	}

	rec := serve(t, s, "/history", "Bearer "+testKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("/history status %d: %s", rec.Code, rec.Body)
	}
	want := `{"accounts":[{"account":"uuid-a","email":"a@example.com"},{"account":"uuid-b","email":"b@example.com"}],` +
		`"samples":[[1800000000,0,"5h",56],[1800000000,0,"weekly",14.5],[1800000000,1,"5h",20]]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("/history =\n%s\nwant\n%s", got, want)
	}
}

func TestHistoryEmpty(t *testing.T) {
	s := newTestService(t, &fakeAccounts{})
	rec := serve(t, s, "/history", "Bearer "+testKey)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"accounts":[],"samples":[]}` {
		t.Fatalf("/history = %d %s", rec.Code, got)
	}
}

func TestUsageReportsFailingAccount(t *testing.T) {
	accounts := &fakeAccounts{
		auths: []*coreauth.Auth{
			claudeAuth("a", "uuid-a", "a@example.com"),
			claudeAuth("b", "uuid-b", "b@example.com"),
		},
		responses: map[string]func(context.Context) (*http.Response, error){
			"a": respond(http.StatusOK, `{"limits": [{"kind": "session", "percent": 56, "resets_at": null}]}`),
			"b": respond(http.StatusTooManyRequests, `{}`),
		},
	}
	s := newTestService(t, accounts)
	rec := serve(t, s, "/usage", "Bearer "+testKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("/usage status %d: %s", rec.Code, rec.Body)
	}
	want := `[{"account":"uuid-a","email":"a@example.com","window":"5h","utilization":56,"resets_at":null,"fetched_at":"2027-01-15T08:00:00Z"},` +
		`{"account":"uuid-b","email":"b@example.com","error":"rate limited"}]`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("/usage =\n%s\nwant\n%s", got, want)
	}

	var count int
	if errQuery := s.store.db.QueryRow(`SELECT COUNT(*) FROM usage_samples`).Scan(&count); errQuery != nil {
		t.Fatalf("count samples: %v", errQuery)
	}
	if count != 0 {
		t.Fatalf("/usage stored %d samples; only the poller stores", count)
	}
}

func TestUsageReasons(t *testing.T) {
	tests := map[int]string{
		http.StatusUnauthorized:        "token expired",
		http.StatusForbidden:           "forbidden",
		http.StatusTooManyRequests:     "rate limited",
		http.StatusInternalServerError: "HTTP 500",
	}
	for status, reason := range tests {
		accounts := &fakeAccounts{
			auths:     []*coreauth.Auth{claudeAuth("a", "uuid-a", "a@example.com")},
			responses: map[string]func(context.Context) (*http.Response, error){"a": respond(status, `{}`)},
		}
		rec := serve(t, newTestService(t, accounts), "/usage", "Bearer "+testKey)
		var got []map[string]any
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &got); errDecode != nil {
			t.Fatalf("decode /usage: %v", errDecode)
		}
		if len(got) != 1 || got[0]["error"] != reason {
			t.Fatalf("status %d gave %v, want error %q", status, got, reason)
		}
	}
}

func TestRoutesRequireHistoryKey(t *testing.T) {
	s := newTestService(t, &fakeAccounts{})
	for _, path := range []string{"/usage", "/history"} {
		for _, authorization := range []string{"", "Bearer wrong", "Bearer client-key", testKey, "Bearer " + testKey + "x"} {
			if rec := serve(t, s, path, authorization); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s with %q: status %d, want 401", path, authorization, rec.Code)
			}
		}
		if rec := serve(t, s, path, "Bearer "+testKey); rec.Code != http.StatusOK {
			t.Fatalf("%s with the history key: status %d, want 200", path, rec.Code)
		}
	}
}

func TestCloseStopsPollerBeforeDatabase(t *testing.T) {
	entered := make(chan struct{})
	var sawCancel bool
	accounts := &fakeAccounts{
		auths: []*coreauth.Auth{claudeAuth("a", "uuid-a", "a@example.com")},
		responses: map[string]func(context.Context) (*http.Response, error){
			"a": func(ctx context.Context) (*http.Response, error) {
				close(entered)
				<-ctx.Done()
				sawCancel = true
				return nil, ctx.Err()
			},
		},
	}
	cfg := config.HistoryConfig{Enabled: true, DBPath: filepath.Join(t.TempDir(), "history.db"), Key: testKey}
	s, errStart := Start(context.Background(), cfg, accounts)
	if errStart != nil {
		t.Fatalf("Start: %v", errStart)
	}
	<-entered
	if errClose := s.Close(); errClose != nil {
		t.Fatalf("Close: %v", errClose)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("poller still running after Close")
	}
	if !sawCancel {
		t.Fatal("in-flight fetch was not cancelled")
	}
	if errPing := s.store.db.Ping(); errPing == nil {
		t.Fatal("database still open after Close")
	}
}
