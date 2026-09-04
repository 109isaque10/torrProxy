package indexers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"strings"
	"time"
	"torrProxy/caching"
	"torrProxy/types"

	"github.com/jellydator/ttlcache/v3"
	"github.com/wasilibs/go-re2"
	"go.uber.org/zap"
)

type CapybaraBRAPIIndexer struct {
	BaseURL   string
	APIKey    string
	Freeleech bool
	Client    *http.Client

	cache *ttlcache.Cache[string, any]
}

var (
	freelechRe = re2.MustCompile(`100%?`)
)

func (c *CapybaraBRAPIIndexer) Name() string {
	return "CapybaraBR (API)"
}

func (c *CapybaraBRAPIIndexer) Id() string {
	return "capybarabr"
}

func (c *CapybaraBRAPIIndexer) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *CapybaraBRAPIIndexer) buildURL() (*neturl.URL, error) {
	u, err := neturl.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "api/torrents/filter")
	return u, nil
}

func (c *CapybaraBRAPIIndexer) Search(ctx context.Context, query, alt string) ([]types.Result, error) {
	qLow := strings.ToLower(query)
	if completRe.MatchString(qLow) {
		return nil, fmt.Errorf("no need to search for packs")
	} else if collectionRe.MatchString(qLow) {
		return nil, fmt.Errorf("no need to search for collections")
	}

	// Check cache first if cache is available
	if c.cache != nil {
		cacheKey := caching.GenerateCacheKey(c.Id(), query)
		if cached := c.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for capybarabr search", zap.String("query", query))
				return results, nil
			}
		}
	}

	u, err := c.buildURL()
	if err != nil {
		return nil, err
	}

	qp := u.Query()
	qp.Set("name", query)
	qp.Set("perPage", "100")
	u.RawQuery = qp.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("capybarabr: bad response %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	resultsRaw := payload["data"]
	if resultsRaw == nil {
		resultsRaw = payload["results"]
	}

	resultsSlice, _ := resultsRaw.([]any)
	if resultsSlice == nil {
		return nil, fmt.Errorf("capybarabr: unexpected json structure")
	}

	out := make([]types.Result, 0, len(resultsSlice))
	for _, ri := range resultsSlice {
		item, _ := ri.(map[string]any)
		if item == nil {
			continue
		}

		attrs := item
		if rawAttrs, ok := item["attributes"]; ok {
			if m, ok := rawAttrs.(map[string]any); ok {
				attrs = m
			}
		}

		// free mapping (api returns false/true) -> map to numeric factor
		free := false
		if raw, ok := attrs["freeleech"]; ok {
			free = freelechRe.MatchString(types.ToString(raw))
		}
		if c.Freeleech && !free {
			return nil, fmt.Errorf("not free")
		}

		title := types.ToString(attrs["name"])
		download := types.ToString(attrs["download_link"])

		// parse created_at with timezone - YAML appended " -03:00" before parsing:
		createdAt := types.ToString(attrs["created_at"])
		if createdAt != "" && !strings.Contains(createdAt, "+") && !strings.Contains(createdAt, "-") {
			// append BRT offset if missing (YAML does this)
			createdAt = createdAt + " -03:00"
		}
		pub := ParseDateWithFormats(createdAt, []string{"01/02/2006 15:04:05 -07:00", time.RFC3339, "2006-01-02T15:04:05.000000Z"})

		res := types.Result{
			Title:       title,
			Link:        types.ToString(attrs["details_link"]),
			Size:        types.ToString(attrs["size"]),
			Free:        free,
			PubDate:     pub,
			Seeders:     toInt(attrs["seeders"]),
			Leechers:    toInt(attrs["leechers"]),
			InfoHash:    types.ToString(attrs["info_hash"]),
			DownloadURL: buildTorrProxyDownloadLink(c.Id(), download),
		}

		out = append(out, res)
	}

	// Cache the results if cache is available
	if c.cache != nil {
		cacheKey := caching.GenerateCacheKey(c.Id(), query)
		c.cache.Set(cacheKey, out, time.Hour)
		zap.L().Debug("💾 Cached capybarabr search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", time.Hour), zap.Int("count", len(out)))
	}

	return out, nil
}

func init() {
	base := defaultEnv("CAPYBARA_BASE", "https://capybarabr.com/")
	apiKey := os.Getenv("CAPYBARA_APIKEY")
	idx := &CapybaraBRAPIIndexer{
		BaseURL: base,
		APIKey:  apiKey,
		cache:   caching.C().Cache,
	}
	types.Indexers = append(types.Indexers, idx)
}
