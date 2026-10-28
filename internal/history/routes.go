package history

import (
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// usageWindow is one window of a successful live fetch in /usage.
type usageWindow struct {
	Account     string     `json:"account"`
	Email       string     `json:"email"`
	Window      string     `json:"window"`
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
	FetchedAt   time.Time  `json:"fetched_at"`
}

// usageError is an account whose live fetch failed in /usage.
type usageError struct {
	Account string `json:"account"`
	Email   string `json:"email"`
	Error   string `json:"error"`
}

type historyAccount struct {
	Account string `json:"account"`
	Email   string `json:"email"`
}

type historyResponse struct {
	Accounts []historyAccount `json:"accounts"`
	// Samples holds [ts in Unix seconds, account index, window, utilization].
	Samples [][4]any `json:"samples"`
}

// Register adds GET /usage and GET /history, both behind the history key.
func (s *Service) Register(engine *gin.Engine) {
	engine.GET("/usage", s.requireKey, s.handleUsage)
	engine.GET("/history", s.requireKey, s.handleHistory)
}

func (s *Service) requireKey(c *gin.Context) {
	got := []byte(c.GetHeader("Authorization"))
	want := []byte("Bearer " + s.key)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.Next()
}

// handleUsage fetches every account live, in parallel, with no cache.
func (s *Service) handleUsage(c *gin.Context) {
	accounts := s.claudeAccounts()
	results := make([][]any, len(accounts))
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Go(func() {
			readings, errFetch := s.fetch(c.Request.Context(), acc)
			if errFetch != nil {
				results[i] = []any{usageError{Account: acc.uuid, Email: acc.email, Error: errFetch.Error()}}
				return
			}
			fetchedAt := s.now().UTC()
			for _, r := range readings {
				results[i] = append(results[i], usageWindow{
					Account:     acc.uuid,
					Email:       acc.email,
					Window:      r.window,
					Utilization: r.utilization,
					ResetsAt:    r.resetsAt,
					FetchedAt:   fetchedAt,
				})
			}
		})
	}
	wg.Wait()
	out := []any{}
	for _, entries := range results {
		out = append(out, entries...)
	}
	c.JSON(http.StatusOK, out)
}

// handleHistory returns every stored sample. Each account is labelled with its latest email.
func (s *Service) handleHistory(c *gin.Context) {
	samples, errAll := s.store.all(c.Request.Context())
	if errAll != nil {
		log.Errorf("history: read samples: %v", errAll)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "history unavailable"})
		return
	}
	resp := historyResponse{Accounts: []historyAccount{}, Samples: make([][4]any, 0, len(samples))}
	index := make(map[string]int)
	for _, smp := range samples {
		i, ok := index[smp.account]
		if !ok {
			i = len(resp.Accounts)
			index[smp.account] = i
			resp.Accounts = append(resp.Accounts, historyAccount{Account: smp.account})
		}
		resp.Accounts[i].Email = smp.email
		resp.Samples = append(resp.Samples, [4]any{smp.ts, i, smp.window, smp.utilization})
	}
	c.JSON(http.StatusOK, resp)
}
