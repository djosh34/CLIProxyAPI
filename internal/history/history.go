// Package history polls the 5h and weekly usage of every Claude OAuth account,
// stores the samples in SQLite and serves them at GET /usage and GET /history.
package history

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const defaultInterval = 5 * time.Minute

// Accounts lists the proxy's auths and sends requests with their credentials.
// *coreauth.Manager implements it.
type Accounts interface {
	List() []*coreauth.Auth
	HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error)
}

// Service polls usage into the store and serves the usage routes.
type Service struct {
	key      string
	accounts Accounts
	store    *store
	now      func() time.Time
	cancel   context.CancelFunc
	done     chan struct{}
}

// account is one Claude OAuth auth with the identity used in the history.
type account struct {
	auth  *coreauth.Auth
	uuid  string
	email string
}

// Start opens the database and starts polling every interval, with the first
// poll right away. It returns a nil Service when history is disabled.
func Start(ctx context.Context, cfg config.HistoryConfig, accounts Accounts) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Key == "" {
		return nil, errors.New("history: enabled but key is empty")
	}
	if !filepath.IsAbs(cfg.DBPath) {
		return nil, fmt.Errorf("history: db-path must be an absolute path, got %q", cfg.DBPath)
	}
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < 0 {
		return nil, fmt.Errorf("history: interval must be positive, got %s", interval)
	}
	st, errOpen := openStore(cfg.DBPath)
	if errOpen != nil {
		return nil, errOpen
	}
	pollCtx, cancel := context.WithCancel(ctx)
	s := &Service{
		key:      cfg.Key,
		accounts: accounts,
		store:    st,
		now:      time.Now,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go s.run(pollCtx, interval)
	log.Infof("history: polling Claude usage every %s", interval)
	return s, nil
}

// Close stops the poller, waits for it to finish and then closes the database.
func (s *Service) Close() error {
	s.cancel()
	<-s.done
	return s.store.close()
}

func (s *Service) run(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// poll fetches every account once and stores what it got. A failed fetch stores nothing.
func (s *Service) poll(ctx context.Context) {
	for _, acc := range s.claudeAccounts() {
		if ctx.Err() != nil {
			return
		}
		readings, errFetch := s.fetch(ctx, acc)
		if errFetch != nil {
			continue
		}
		if errInsert := s.store.insert(ctx, s.now(), acc, readings); errInsert != nil {
			log.Warnf("history: store usage for %s: %v", acc.email, errInsert)
		}
	}
}

// claudeAccounts returns the enabled Claude OAuth accounts, sorted by email.
func (s *Service) claudeAccounts() []account {
	var out []account
	for _, auth := range s.accounts.List() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		if !strings.EqualFold(auth.Provider, "claude") || auth.AuthKind() != coreauth.AuthKindOAuth {
			continue
		}
		_, email := auth.AccountInfo()
		uuid, _ := auth.Metadata["account_uuid"].(string)
		if uuid == "" {
			uuid = auth.ID
		}
		out = append(out, account{auth: auth, uuid: uuid, email: email})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].email < out[j].email })
	return out
}
