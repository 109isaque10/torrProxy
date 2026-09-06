package indexers

import (
	"context"
	"fmt"
	"net/url"
	"net/http"
	"regexp"
	"strings"
	"time"

	"torrProxy/caching"
	"torrProxy/types"

	"github.com/PuerkitoBio/goquery"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

type BaixeTorrents struct {
	BaseURL string

	cache *ttlcache.Cache[string, any]
}

const POSTSLIMIT = 10

func init() {
	types.Indexers = append(types.Indexers, &BaixeTorrents{"www.baixetorrentsv2.net", caching.C().Cache})
}

func (b *BaixeTorrents) Name() string {
	return "BaixeTorrents"
}

func (b *BaixeTorrents) Id() string {
	return "baixetorrents"
}

func (b *BaixeTorrents) Search(ctx context.Context, query, alt string) ([]types.Result, error) {
	// Check cache first if cache is available
	if b.cache != nil {
		cacheKey := caching.GenerateCacheKey(b.Id(), query)
		if cached := b.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for baixetorrents search", zap.String("query", query))
				return results, nil
			}
		}
	}

	searchURL := fmt.Sprintf("https://%s/?s=%s", b.BaseURL, url.QueryEscape(query))

	zap.L().Debug("search", zap.String("searchurl",searchURL))
	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating search request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error executing search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error parsing search page HTML: %w", err)
	}

	var results []types.Result

	doc.Find("div.item div.title a").EachWithBreak(func(i int, s *goquery.Selection) bool {
		select {
		case <-ctx.Done():
			return true
		default:
		}

		if i > POSTSLIMIT {
			return false
		}

		title := strings.TrimSpace(s.Text())
		if title == "" || !IsValidPrefix(query, "", title) {
			zap.L().Debug("invalid title", zap.String("title", title))
			return true
		}

		pageURL, exists := s.Attr("href")
		if !exists || pageURL == "" {
			return true
		}

		torrents, err := b.parseDetailPage(ctx, pageURL, title)
		if err == nil && len(torrents) > 0 {
			results = append(results, torrents...)
		}

		return true
	})

	// Cache the results if cache is available
	if b.cache != nil {
		cacheKey := caching.GenerateCacheKey(b.Id(), query)
		b.cache.Set(cacheKey, results, 2*time.Hour)
		zap.L().Debug("💾 Cached baixetorrents search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", 2*time.Hour), zap.Int("count", len(results)))
	}

	return results, nil
}

func (b *BaixeTorrents) parseDetailPage(ctx context.Context, detailURL string, baseTitle string) ([]types.Result, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", detailURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code %d for detail page", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var results []types.Result
	pageDesc := doc.Find("meta[name='description']").AttrOr("content", "")

	doc.Find("a[href^='magnet:']").Each(func(i int, s *goquery.Selection) {
		magnetLink, exists := s.Attr("href")
		if !exists || magnetLink == "" {
			return
		}

		itemTitle := strings.TrimSpace(s.Text())
		if itemTitle == "" || strings.EqualFold(itemTitle, "Download") || strings.EqualFold(itemTitle, "Baixar") {
			itemTitle = strings.TrimSpace(s.Parent().Text())
		}

		if itemTitle == "" || len(itemTitle) > 100 {
			itemTitle = baseTitle
		} else if !strings.Contains(itemTitle, baseTitle) {
			itemTitle = fmt.Sprintf("%s - %s", baseTitle, itemTitle)
		}

		size := extractSize(s.Text() + " " + s.Parent().Text() + " " + pageDesc)

		results = append(results, types.Result{
			Title:       itemTitle,
			DownloadURL: magnetLink,
			Description: pageDesc,
			Size:        size,
			Link:        detailURL,
		})
	})

	return results, nil
}

func extractSize(text string) string {
	re := regexp.MustCompile(`(?i)\b(\d+(?:[\.,]\d+)?\s*(?:GB|MB|TB|KB))\b`)
	match := re.FindStringSubmatch(text)
	if len(match) > 1 {
		return strings.ToUpper(strings.TrimSpace(match[1]))
	}
	return ""
}
