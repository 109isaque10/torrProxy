package indexers

// Converted & extended from torrent-yml
// Config from environment:
//  - REDE_TORRENT_BASE (default: http://192.168.1.179:4949)
// This indexer calls /indexers/rede_torrent and expects JSON with results array.

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"torrProxy/types"

	"github.com/PuerkitoBio/goquery"
	"github.com/coregx/coregex"
)

var infoHashRe = coregex.MustCompile(`xt=urn:btih:([a-fA-F0-9]{40})`)
var magnetDnRe = coregex.MustCompile(`dn=([^&]+)`)
var seasonRe = coregex.MustCompile(`(?i)(S0)(\d{1,2})$`)

type RedeTorrent struct {
	BaseURL string
	Client  *http.Client
}

func (r *RedeTorrent) Name() string {
	return "Rede Torrent"
}

func (r *RedeTorrent) Id() string {
	return "redetorrent"
}

func (r *RedeTorrent) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (r *RedeTorrent) buildURL() (string, error) {
	u, err := neturl.Parse(r.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "index.php")
	return u.String(), nil
}

// keywordPreprocess performs the YAML filters: tolower, replace " complet" -> "", season S0/S -> "temporada X"
func (r *RedeTorrent) keywordPreprocess(q string) string {
	s := strings.ToLower(q)
	s = strings.ReplaceAll(s, " complet", "")
	// S0(\d{1,2})$ -> temporada $2
	s = seasonRe.ReplaceAllString(s, "$2ª temporada")
	return s
}

func (r *RedeTorrent) Search(ctx context.Context, query string) ([]types.Result, error) {
	url, err := r.buildURL()
	if err != nil {
		return nil, err
	}
	u, _ := neturl.Parse(url)

	qp := u.Query()
	qp.Set("s", r.keywordPreprocess(query))
	u.RawQuery = qp.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := r.client().Do(req)
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

	query = r.FormatQuery(query)
	// Extract links from search results (.capa_lista elements)
	var links []string
	doc.Find(".capa_lista a").Each(func(i int, s *goquery.Selection) {
		if title, exists := s.Attr("title"); exists {
			if !strings.Contains(strings.ToLower(title), query) {
				return
			}
		}
		if href, exists := s.Attr("href"); exists {
			links = append(links, href)
		}
	})

	// // Now scrape each detail page
	// resultsCh := make(chan []types.Result)
	// var wg sync.WaitGroup
	// semaphore := make(chan struct{}, 5) // limit concurrency

	// seen := make(map[string]struct{})
	// for _, link := range links {
	// 	wg.Add(1)
	// 	go func(link string) {
	// 		defer wg.Done()
	// 		semaphore <- struct{}{}
	// 		defer func() { <-semaphore }()
	// 		item, err := r.scrapeDetailPage(ctx, link, seen)
	// 		resultsCh <- item
	// 		if err != nil {
	// 			return
	// 		}
	// 	}(link)
	// }

	// go func() {
	// 	wg.Wait()
	// 	close(resultsCh)
	// }()

	// var results []types.Result
	// for item := range resultsCh {
	// 	results = append(results, item...)
	// }

	// Enqueue and process links with a queued semaphore
	results := r.processLinksWithQueue(ctx, links)

	return results, nil
}

func (r *RedeTorrent) scrapeDetailPage(ctx context.Context, url string, seen map[string]struct{}, mu *sync.Mutex) ([]types.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := http.DefaultClient.Do(req)
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
			TorrentURL:  magnet,
		})
	}

	return results, nil
}

func ExtractInfoHash(magnet string) string {
	// magnet:?xt=urn:btih:INFOHASH&dn=...
	matches := infoHashRe.FindStringSubmatch(magnet)
	if len(matches) > 1 {
		return strings.ToLower(matches[1])
	}
	return ""
}

func (r *RedeTorrent) FormatQuery(q string) string {
	// For TV shows, convert "S01E02" to "S0X02" to match site format
	q = strings.ToLower(q)
	q = strings.ReplaceAll(q, " complet", "")
	q = seasonRe.ReplaceAllString(q, "")
	return q
}

func init() {
	base := os.Getenv("REDE_TORRENT_BASE")
	if base == "" {
		base = "https://redetorrent.com"
	}
	idx := &RedeTorrent{
		BaseURL: base,
	}
	types.Indexers = append(types.Indexers, idx)
}

func (r *RedeTorrent) processLinksWithQueue(ctx context.Context, links []string) []types.Result {
	resultsCh := make(chan []types.Result)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5)       // Limit concurrency - 5 simultaneous requests
	queue := make(chan string, len(links)+10) // Add queue for waiting requests

	// Enqueue all links
	go func() {
		for _, link := range links {
			queue <- link
		}
		close(queue) // Mark the queue as complete
	}()

	var mu sync.Mutex
	seen := make(map[string]struct{})
	for link := range queue {
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
