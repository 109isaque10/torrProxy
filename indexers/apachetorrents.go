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

type ApacheTorrent struct {
	BaseIndexer

	cache *ttlcache.Cache[string, any]
}

func (a *ApacheTorrent) Name() string {
	return "Apache Torrent"
}

func (a *ApacheTorrent) Id() string {
	return "apachetorrent"
}

func (a *ApacheTorrent) IsEnabled() bool {
	return a.BaseIndexer.IsEnabled()
}

func (a *ApacheTorrent) Ping(ctx context.Context) bool {
	return a.BaseIndexer.Ping(ctx, a.Name())
}

func (a *ApacheTorrent) buildSearchURL(query string) string {
	u, _ := neturl.Parse(a.BaseURL)
	u.Path = path.Join(u.Path, "index.php")
	url := u.String()
	u, _ = neturl.Parse(url)

	qp := u.Query()
	qp.Set("s", query)
	u.RawQuery = qp.Encode()
	return u.String()
}

func (a *ApacheTorrent) Search(ctx context.Context, originalQuery, alt string) ([]types.Result, error) {
	if collectionRe.MatchString(strings.ToLower(originalQuery)) {
		return nil, fmt.Errorf("no need to search for collections")
	}

	query, year := keywordPreprocess(originalQuery)

	// Check cache first if cache is available
	if a.cache != nil {
		cacheKey := caching.GenerateCacheKey(a.Id(), query)
		if cached := a.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for ApacheTorrent search", zap.String("query", query))
				return results, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.buildSearchURL(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ApacheTorrent: bad response %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	query = formatQuery(query)
	// Extract links from search results (.capa_lista elements)
	var links []string
	doc.Find(".capaname").Each(func(i int, s *goquery.Selection) {
		aTag := s.Find("h2 a")
		href, hasHref := aTag.Attr("href")
		title, hasTitle := aTag.Attr("title")

		if !hasHref || !hasTitle {
			return
		}
		// Perform title validation checks
		if !IsValidPrefix(query, year, title) || !seasonRe.MatchString(originalQuery) && strings.Contains(title, "emporada") {
			return
		}
		links = append(links, href)
	})

	// Enqueue and process links with a queued semaphore
	results := a.processLinksWithQueue(ctx, links)

	// Cache the results if cache is available
	if a.cache != nil {
		cacheKey := caching.GenerateCacheKey(a.Id(), query)
		a.cache.Set(cacheKey, results, 5*time.Hour)
		zap.L().Debug("💾 Cached ApacheTorrent search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", 5*time.Hour), zap.Int("count", len(results)))
	}

	return results, nil
}

func (a *ApacheTorrent) scrapeDetailPage(ctx context.Context, url string, seen map[string]struct{}, mu *sync.Mutex) ([]types.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ApacheTorrent: bad response %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	// Extract magnet links - look for all <a href="magnet:...">
	var magnets []string
	doc.Find("div#lista_links > div#download > p.text-center > a").Each(func(i int, s *goquery.Selection) {
		magnetLink, exists := s.Attr("href")
		if !exists || magnetLink == "" {
			return
		}
		magnets = append(magnets, magnetLink)
		itemTitle, exists := s.Attr("title")
		if !exists || itemTitle == "" {
			return
		}
	})

	var size string
	doc.Find("div.infos > p").Find("strong").Each(func(i int, s *goquery.Selection) {
		key := strings.TrimSpace(strings.TrimSuffix(s.Text(), ":"))

		// Extract value from the text node right after <strong>
		var val string
		if len(s.Nodes) > 0 && s.Nodes[0].NextSibling != nil {
			val = strings.TrimSpace(s.Nodes[0].NextSibling.Data)
			val = strings.TrimSpace(strings.TrimPrefix(val, ":"))
		}

		switch key {
		case "Tamanho":
			if val == "" || strings.EqualFold(val, "Desconhecido") {
				size = "0"
			} else {
				size, _, _ = strings.Cut(strings.TrimSuffix(strings.TrimPrefix(val, "Tamanho: "), " GB"), " /")
				size = strings.TrimSpace(size)
			}
		}
	})

	var description string
	doc.Find("div.row > div.col-12 > p.sinopse").Each(func(i int, s *goquery.Selection) {
		description = strings.TrimSpace(s.Text())
		description = strings.TrimPrefix(description, "Sinopse:")
		description = strings.TrimSpace(description)
	})

	date := getMetaContent(doc, `meta[property="og:updated_time"]`)
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
			Description: description,
			InfoHash:    infoHash,
			Size:        size,
			PubDate:     pub,
			DownloadURL: magnet,
		})
	}

	return results, nil
}

func init() {
	idx := &ApacheTorrent{
		BaseIndexer: BaseIndexer{
			BaseURL: "https://apachetorrent.com",
			Client:  &http.Client{Timeout: 15 * time.Second},
		},
		cache: caching.C().Cache,
	}
	idx.IsAlive.Store(true)
	idx.IsAuthenticated.Store(true) // No need for auth

	types.Indexers = append(types.Indexers, idx)
}

func (a *ApacheTorrent) processLinksWithQueue(ctx context.Context, links []string) []types.Result {
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
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}

			item, err := a.scrapeDetailPage(ctx, link, seen, &mu)
			if err == nil && len(item) > 0 {
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
