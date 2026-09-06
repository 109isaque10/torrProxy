package indexers

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"path"
	"strings"
	"sync"
	"time"
	"torrProxy/caching"
	"torrProxy/types"

	"github.com/PuerkitoBio/goquery"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

type RedeTorrent struct {
	BaseIndexer

	cache *ttlcache.Cache[string, any]
}

func (r *RedeTorrent) Name() string {
	return "Rede Torrent"
}

func (r *RedeTorrent) Id() string {
	return "redetorrent"
}

func (r *RedeTorrent) IsEnabled() bool {
	return r.BaseIndexer.IsEnabled()
}

func (r *RedeTorrent) Ping(ctx context.Context) bool {
	return r.BaseIndexer.Ping(ctx, r.Name())
}

func (r *RedeTorrent) buildURL() (string, error) {
	u, err := neturl.Parse(r.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "index.php")
	return u.String(), nil
}

func (r *RedeTorrent) Search(ctx context.Context, originalQuery, alt string) ([]types.Result, error) {
	if collectionRe.MatchString(strings.ToLower(originalQuery)) {
		return nil, fmt.Errorf("no need to search for collections")
	}

	query, year := keywordPreprocess(originalQuery)

	// Check cache first if cache is available
	if r.cache != nil {
		cacheKey := caching.GenerateCacheKey(r.Id(), query)
		if cached := r.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for redetorrent search", zap.String("query", query))
				return results, nil
			}
		}
	}

	url, err := r.buildURL()
	if err != nil {
		return nil, err
	}
	u, _ := neturl.Parse(url)

	qp := u.Query()
	qp.Set("s", query)
	u.RawQuery = qp.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("redetorrent: bad response %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	query = formatQuery(query)
	// Extract links from search results (.capa_lista elements)
	var links []string
	doc.Find(".capa_lista a").Each(func(i int, s *goquery.Selection) {
		if title, exists := s.Attr("title"); exists {
			if !IsValidPrefix(query, year, title) || !seasonRe.MatchString(originalQuery) && strings.Contains(title, "emporada") {
				return
			}
		}
		if href, exists := s.Attr("href"); exists {
			links = append(links, href)
		}
	})

	// Enqueue and process links with a queued semaphore
	results := r.processLinksWithQueue(ctx, links)

	// Cache the results if cache is available
	if r.cache != nil {
		cacheKey := caching.GenerateCacheKey(r.Id(), query)
		r.cache.Set(cacheKey, results, 5*time.Hour)
		zap.L().Debug("💾 Cached redetorrent search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", 5*time.Hour), zap.Int("count", len(results)))
	}

	return results, nil
}

func (r *RedeTorrent) scrapeDetailPage(ctx context.Context, url string, seen map[string]struct{}, mu *sync.Mutex) ([]types.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("redetorrent: bad response %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	// Extract magnet links - look for all <a href="magnet:...">
	var magnets []string
	doc.Find("a[href^='magnet:']").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if exists {
			magnets = append(magnets, href)
		}
	})

	var size string
	var originalTitle string
	var date string
	doc.Find("div#informacoes > p").Each(func(i int, s *goquery.Selection) {
		for line := range strings.Lines(s.Text()) {
			line = strings.TrimSpace(line)

			if strings.Contains(line, "Tamanho:") {
				size := strings.TrimSuffix(strings.TrimPrefix(line, "Tamanho: "), " GB")
				if size == "Desconhecido" {
					size = "0"
				} else {
					size += " GB"
				}
				continue
			}
			if strings.Contains(line, "Título Original:") {
				originalTitle = strings.TrimPrefix(line, "Título Original: ")
				continue
			}
		}
	})

	doc.Find(".data_post a time").Each(func(i int, s *goquery.Selection) {
		if dateAttr, exists := s.Attr("datetime"); exists {
			date = dateAttr
		}
	})

	pub := ParseDateWithFormats(date, []string{time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05", "02/01/2006 15:04:05"})

	// Build results
	var results []types.Result
	for _, magnet := range magnets {
		infoHash := ExtractInfoHash(magnet)
		mu.Lock()
		if _, exists := seen[infoHash]; exists {
			mu.Unlock()
			continue
		}
		seen[infoHash] = struct{}{}
		mu.Unlock()
		var title string
		matches := magnetDnRe.FindStringSubmatch(magnet)
		if len(matches) > 1 {
			title, _ = neturl.QueryUnescape(matches[1])
		}

		results = append(results, types.Result{
			Title:       title,
			Link:        url,
			Description: originalTitle,
			InfoHash:    infoHash,
			Size:        size,
			PubDate:     pub,
			DownloadURL: magnet,
		})
	}

	return results, nil
}

func init() {
	idx := &RedeTorrent{
		BaseIndexer: BaseIndexer{
			BaseURL: defaultEnv("REDE_TORRENT_BASE", "https://redetorrent.com"),
			Client:  &http.Client{Timeout: 20 * time.Second},
		},
		cache: caching.C().Cache,
	}
	idx.IsAlive.Store(true)

	types.Indexers = append(types.Indexers, idx)
}

func (r *RedeTorrent) processLinksWithQueue(ctx context.Context, links []string) []types.Result {
	resultsCh := make(chan []types.Result)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5) // Limit concurrency - 5 simultaneous requests
	// queue := make(chan string, len(links)+10) // Add queue for waiting requests

	// Enqueue all links
	// go func() {
	// 	for _, link := range links {
	// 		queue <- link
	// 	}
	// 	close(queue) // Mark the queue as complete
	// }()

	var mu sync.Mutex
	seen := make(map[string]struct{})
	for _, link := range links {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			semaphore <- struct{}{}        // Wait for semaphore (blocks if full)
			defer func() { <-semaphore }() // Signal the semaphore is free

			item, err := r.scrapeDetailPage(ctx, link, seen, &mu)
			if err == nil {
				resultsCh <- item
			}
		}(link)
	}

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
	return results
}
