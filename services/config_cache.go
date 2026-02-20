package services

import (
	"context"
	"sync"
	"time"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

// CachedConfigLoader wraps a ConfigLoader and caches the resolved *YAMLConfig
// for a configurable TTL. Subsequent calls within the TTL window return the
// cached config without making any GitHub API calls.
//
// The cache is safe for concurrent access. It is keyed on the effective config
// file path, so different configs don't collide.
type CachedConfigLoader struct {
	inner ConfigLoader
	ttl   time.Duration

	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	config    *types.YAMLConfig
	fetchedAt time.Time
}

// NewCachedConfigLoader wraps inner with a TTL cache.
// A TTL of 0 disables caching (every call delegates directly).
func NewCachedConfigLoader(inner ConfigLoader, ttl time.Duration) ConfigLoader {
	if ttl <= 0 {
		return inner
	}
	return &CachedConfigLoader{
		inner:   inner,
		ttl:     ttl,
		entries: make(map[string]*cacheEntry),
	}
}

// LoadConfig returns a cached config if fresh, otherwise delegates to the inner loader.
func (c *CachedConfigLoader) LoadConfig(ctx context.Context, config *configs.Config) (*types.YAMLConfig, error) {
	key := config.EffectiveConfigFile()

	// Fast path: check under read lock
	c.mu.RLock()
	if entry, ok := c.entries[key]; ok && time.Since(entry.fetchedAt) < c.ttl {
		c.mu.RUnlock()
		LogInfoCtx(ctx, "using cached workflow config", map[string]interface{}{
			"config_file": key,
			"age_seconds": int(time.Since(entry.fetchedAt).Seconds()),
			"ttl_seconds": int(c.ttl.Seconds()),
		})
		return entry.config, nil
	}
	c.mu.RUnlock()

	// Slow path: fetch and cache
	result, err := c.inner.LoadConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[key] = &cacheEntry{config: result, fetchedAt: time.Now()}
	c.mu.Unlock()

	LogInfoCtx(ctx, "cached workflow config", map[string]interface{}{
		"config_file": key,
		"ttl_seconds": int(c.ttl.Seconds()),
	})

	return result, nil
}

// LoadConfigFromContent delegates directly — content-based loads are not cached
// since the caller already has the content in hand.
func (c *CachedConfigLoader) LoadConfigFromContent(content string, filename string) (*types.YAMLConfig, error) {
	return c.inner.LoadConfigFromContent(content, filename)
}

// InvalidateCache clears all cached entries. Useful after a config-repo push event.
func (c *CachedConfigLoader) InvalidateCache() {
	c.mu.Lock()
	c.entries = make(map[string]*cacheEntry)
	c.mu.Unlock()
	LogInfo("workflow config cache invalidated")
}
