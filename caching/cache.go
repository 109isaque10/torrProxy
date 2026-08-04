package caching

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

type CacheInstance struct {
	Cache *ttlcache.Cache[string, any]
	mu    sync.RWMutex
}

type cacheData map[string]struct {
	Value any
	TTL   time.Duration
}

var globalCache *CacheInstance

// NewCache creates a new cache instance
func newCache() *CacheInstance {
	c := ttlcache.New[string, any](ttlcache.WithDisableTouchOnHit[string, any]())
	cacheInstance := &CacheInstance{
		Cache: c,
		mu:    sync.RWMutex{},
	}

	// Start periodic cleanup
	go c.Start()

	return cacheInstance
}

func C() *CacheInstance {
	if globalCache == nil {
		globalCache = newCache()
	}
	return globalCache
}

// generateCacheKey generates a cache key for a search query
func GenerateCacheKey(id, query string) string {
	hash := sha256.Sum256([]byte(query))
	return fmt.Sprintf("%s_search_%x", id, hash)
}
