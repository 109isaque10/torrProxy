package indexers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"torrProxy/caching"
	"torrProxy/types"

	"github.com/PuerkitoBio/goquery"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

type BaixeTorrents struct {
	BaseIndexer

	cache *ttlcache.Cache[string, any]
}

const POSTSLIMIT = 10

func init() {
	idx := &BaixeTorrents{BaseIndexer{BaseURL: "https://www.baixetorrentsv2.net", Client: &http.Client{Timeout: 15 * time.Second}}, caching.C().Cache}
	idx.IsAlive.Store(true)
	idx.IsAuthenticated.Store(true) // No need for auth
	types.Indexers = append(types.Indexers, idx)
}

func (b *BaixeTorrents) Name() string {
	return "BaixeTorrents"
}

func (b *BaixeTorrents) Id() string {
	return "baixetorrents"
}

func (b *BaixeTorrents) IsEnabled() bool {
	return b.BaseIndexer.IsEnabled()
}

func (b *BaixeTorrents) Ping(ctx context.Context) bool {
	return b.BaseIndexer.Ping(ctx, b.Name())
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

	searchURL := fmt.Sprintf("%s/?s=%s", b.BaseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating search request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := b.Client.Do(req)
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

	resultsCh := make(chan []types.Result)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5)
	var mu sync.Mutex
	seen := make(map[string]struct{})
	doc.Find("div.item div.title a").EachWithBreak(func(i int, s *goquery.Selection) bool {
		if i > POSTSLIMIT {
			return false
		}

		title := strings.TrimSpace(s.Text())
		if title == "" || !IsValidPrefix(query, "", title) {
			return true
		}

		pageURL, exists := s.Attr("href")
		if !exists || pageURL == "" {
			return true
		}

		wg.Add(1)
		go func(urlStr, tStr string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}

			torrents, err := b.parseDetailPage(ctx, urlStr, tStr, seen, &mu)
			if err == nil && len(torrents) > 0 {
				select {
				case resultsCh <- torrents:
				case <-ctx.Done():
				}
			}
		}(pageURL, title)

		return true
	})

	// Close results channel once all goroutines complete
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results
	var results []types.Result
	for item := range resultsCh {
		results = append(results, item...)
	}

	// Cache the results if cache is available
	if b.cache != nil {
		cacheKey := caching.GenerateCacheKey(b.Id(), query)
		b.cache.Set(cacheKey, results, 2*time.Hour)
		zap.L().Debug("💾 Cached baixetorrents search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", 2*time.Hour), zap.Int("count", len(results)))
	}

	return results, nil
}

func (b *BaixeTorrents) parseDetailPage(ctx context.Context, detailURL, baseTitle string, seen map[string]struct{}, mu *sync.Mutex) ([]types.Result, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", detailURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := b.Client.Do(req)
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

		infoHash := ExtractInfoHash(magnetLink)
		mu.Lock()
		if _, exists := seen[infoHash]; exists {
			mu.Unlock()
			return
		}
		seen[infoHash] = struct{}{}
		mu.Unlock()

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
			InfoHash:    infoHash,
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
