// indexers/utils_2.go
package indexers

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type BaseIndexer struct {
	BaseURL         string
	IsAuthenticated atomic.Bool
	Client          *http.Client
	IsAlive         atomic.Bool
}

func (b *BaseIndexer) IsEnabled() bool {
	return b.IsAlive.Load()
}

// Default Ping sends a lightweight HEAD or GET request to the indexer's BaseURL
func (b *BaseIndexer) Ping(ctx context.Context, name string) bool {
	if b.BaseURL == "" || b.IsAuthenticated.Load() == false {
		return false
	}

	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	// Try HEAD first for lightweight checks
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, b.BaseURL, nil)
	if err != nil {
		b.IsAlive.Store(false)
		return false
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 500 {
		// Fallback to GET if HEAD method is forbidden (405) by the site
		if resp != nil && resp.StatusCode == http.StatusMethodNotAllowed {
			defer resp.Body.Close()

			req.Method = http.MethodGet
			resp, err = client.Do(req)
		}
	}

	if err != nil || (resp != nil && resp.StatusCode >= 500) {
		if b.IsAlive.CompareAndSwap(true, false) {
			zap.L().Warn("🚫 Indexer marked offline", zap.String("indexer", name), zap.Error(err))
		}
		defer resp.Body.Close()
		return false
	}
	defer resp.Body.Close()

	if b.IsAlive.CompareAndSwap(false, true) {
		zap.L().Info("✔️ Indexer recovered and reactivated", zap.String("indexer", name))
	}
	return true
}
