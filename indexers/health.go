package indexers

import (
	"context"
	"time"
	"torrProxy/types"
)

type HealthChecker struct {
	interval time.Duration
}

func NewHealthChecker(interval time.Duration) *HealthChecker {
	return &HealthChecker{interval: interval}
}

func (hc *HealthChecker) Start(ctx context.Context) {
	hc.checkAll(ctx)
	ticker := time.NewTicker(hc.interval)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hc.checkAll(ctx)
			}
		}
	}()
}

func (hc *HealthChecker) checkAll(parentCtx context.Context) {
	for _, idx := range types.Indexers {
		go func(i types.Indexer) {
			pingCtx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
			defer cancel()

			i.Ping(pingCtx)
		}(idx)
	}
}
