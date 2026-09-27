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
	if b.BaseURL == "" {
		return false
	} else if b.IsAuthenticated.Load() == false {
		if b.IsAlive.CompareAndSwap(true, false) {
			zap.L().Warn("🚫 Indexer marked offline for auth reasons", zap.String("indexer", name))
		}
		return false
	}

	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	do := func(method string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, b.BaseURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "torrProxy/1.0")
		return client.Do(req)
	}

	resp, err := do(http.MethodHead)
	if err != nil || resp.StatusCode == http.StatusMethodNotAllowed {
		// Fallback to GET if HEAD method is forbidden (405) by the site
		resp.Body.Close()
		resp, err = do(http.MethodGet)
	}

	if err != nil {
		if b.IsAlive.CompareAndSwap(true, false) {
			zap.L().Warn("🚫 Indexer marked offline", zap.String("indexer", name), zap.Error(err))
		}
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		if b.IsAlive.CompareAndSwap(true, false) {
			zap.L().Warn("🚫 Indexer marked offline", zap.String("indexer", name), zap.Int("status", resp.StatusCode))
		}
		return false
	}

	if b.IsAlive.CompareAndSwap(false, true) {
		zap.L().Info("✔️ Indexer recovered and reactivated", zap.String("indexer", name))
	}
	return true
}
