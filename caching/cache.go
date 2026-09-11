package caching

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/jellydator/ttlcache/v3"
)

type CacheInstance struct {
	Cache *ttlcache.Cache[string, any]
}

var globalCache *CacheInstance

func init() {
	globalCache = newCache()
}

// NewCache creates a new cache instance
func newCache() *CacheInstance {
	c := ttlcache.New(ttlcache.WithDisableTouchOnHit[string, any]())
	cacheInstance := &CacheInstance{
		Cache: c,
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
	query = strings.ToLower(strings.TrimSpace(query))
	hash := sha256.Sum256([]byte(query))
	return fmt.Sprintf("%s_search_%x", id, hash)
}
