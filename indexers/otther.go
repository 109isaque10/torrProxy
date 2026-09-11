package indexers

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"torrProxy/caching"
	"torrProxy/types"

	"github.com/goccy/go-json"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

type Otther struct {
	BaseIndexer

	Username string
	Password string

	loginOnce sync.Once
	loginErr  error

	cache *ttlcache.Cache[string, any]
}

func init() {
	jar, _ := cookiejar.New(nil)

	idx := &Otther{
		BaseIndexer: BaseIndexer{
			BaseURL: "https://otther.org",
			Client: &http.Client{
				Jar:     jar,
				Timeout: 15 * time.Second,
			},
		},
		Username: defaultEnv("OTTHER_USERNAME", ""),
		Password: defaultEnv("OTTHER_PASSWORD", ""),
		cache:    caching.C().Cache,
	}

	if idx.Username == "" || idx.Password == "" {
		return
	}

	idx.IsAlive.Store(true)
	idx.IsAuthenticated.Store(true) // Checks auth after

	types.Indexers = append(types.Indexers, idx)
}

func (o *Otther) Name() string {
	return "Otther"
}

func (o *Otther) Id() string {
	return "otther"
}

func (o *Otther) IsEnabled() bool {
	return o.BaseIndexer.IsEnabled()
}

func (o *Otther) SetAuth(e bool) {
	o.BaseIndexer.IsAuthenticated.Store(e)
}

func (o *Otther) Ping(ctx context.Context) bool {
	return o.BaseIndexer.Ping(ctx, o.Name())
}

// Structs for API Requests & Responses

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Ok bool `json:"ok"`
}

type ottherSearchResult struct {
	Results []struct {
		ID   string `json:"id"`
		Nome string `json:"nome"`
	} `json:"results"`
}

type ottherPost struct {
	ID          string    `json:"id"`
	PublishedAt time.Time `json:"publicadoEm"`
	FileSize    string    `json:"fileSize"`
	Ficha       struct {
		Titulo    string `json:"titulo"`
		Descricao string `json:"descricao"`
	} `json:"ficha"`
	UploadTitle string `json:"uploadTitle"`
	Links       []struct {
		URL    string `json:"url"`
		Status string `json:"status"`
	} `json:"links,omitempty"`
	MagnetLink string `json:"magnetLink,omitempty"`
}

// Authenticate performs login once and stores cookies in the client jar
func (o *Otther) EnsureLoggedIn() error {
	if o.Username == "" || o.Password == "" {
		return nil
	}

	o.loginOnce.Do(func() {
		authCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		loginURL := fmt.Sprintf("%s/api/auth/login", o.BaseURL)

		payload, err := json.Marshal(loginRequest{
			Username: o.Username,
			Password: o.Password,
		})
		if err != nil {
			o.loginErr = fmt.Errorf("failed to marshal login payload: %w", err)
			return
		}

		req, err := http.NewRequestWithContext(authCtx, http.MethodPost, loginURL, bytes.NewBuffer(payload))
		if err != nil {
			o.loginErr = fmt.Errorf("failed to create login request: %w", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := o.Client.Do(req)
		if err != nil {
			o.loginErr = fmt.Errorf("login request failed: %w", err)
			return
		}
		defer resp.Body.Close()

		// if resp.StatusCode != http.StatusOK {
		// 	o.loginErr = fmt.Errorf("unexpected status code on login: %d", resp.StatusCode)
		// 	return
		// }

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			o.loginErr = fmt.Errorf("unexpected status code on login: %d, body: %s", resp.StatusCode, string(bodyBytes))
			return
		}

		var res loginResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			o.loginErr = fmt.Errorf("failed to decode login response: %w", err)
			return
		}

		if !res.Ok {
			o.loginErr = fmt.Errorf("login response returned ok: false")
			return
		}

		zap.L().Info("✅ Otther authentication successful")
	})

	return o.loginErr
}

func (o *Otther) Search(ctx context.Context, query, alt string) ([]types.Result, error) {
	q := cmp.Or(alt, query)

	// Check cache first if cache is available
	if o.cache != nil {
		cacheKey := caching.GenerateCacheKey(o.Id(), q)
		if cached := o.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for otther search", zap.String("query(alt)", alt))
				return results, nil
			}
		}
	}

	searchURL := fmt.Sprintf("%s/api/search?q=%s", o.BaseURL, url.QueryEscape(q))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code on search: %d\nbody: %s", resp.StatusCode, string(body))
	}

	var searchData ottherSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&searchData); err != nil {
		return nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	resultsCh := make(chan []types.Result)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5)

	var mu sync.Mutex
	seen := make(map[string]struct{})

	for _, item := range searchData.Results {
		if item.ID == "" {
			continue
		}

		mu.Lock()
		if _, exists := seen[item.ID]; exists {
			mu.Unlock()
			continue
		}
		seen[item.ID] = struct{}{}
		mu.Unlock()

		wg.Add(1)
		go func(id, name string) {
			defer wg.Done()

			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}

			postResult, err := o.fetchPostDetails(ctx, id, name)
			if err != nil {
				zap.L().Debug("got error on post details", zap.Error(err), zap.String("id", id), zap.String("name", name))
				return
			}

			if len(postResult) > 0 {
				select {
				case resultsCh <- postResult:
				case <-ctx.Done():
					return
				}
			}
		}(item.ID, item.Nome)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var results []types.Result
	for item := range resultsCh {
		results = append(results, item...)
	}

	// Cache the results if cache is available
	if o.cache != nil {
		cacheKey := caching.GenerateCacheKey(o.Id(), q)
		o.cache.Set(cacheKey, results, 2*time.Hour)
		zap.L().Debug("💾 Cached otther search results", zap.String("query", q), zap.String("key", cacheKey), zap.Duration("ttl", 2*time.Hour), zap.Int("count", len(results)))
	}

	return results, nil
}

func (o *Otther) fetchPostDetails(ctx context.Context, postID, searchName string) ([]types.Result, error) {
	postURL := fmt.Sprintf("%s/api/posts/%s", o.BaseURL, postID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, postURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code %d for post %s", resp.StatusCode, postID)
	}

	var post ottherPost
	if err := json.NewDecoder(resp.Body).Decode(&post); err != nil {
		return nil, err
	}

	title := post.UploadTitle
	if title == "" {
		title = searchName
	}

	var results []types.Result

	for _, l := range post.Links {
		if strings.EqualFold(l.Status, "offline") {
			continue
		}

		results = append(results, types.Result{
			Title:       title,
			Link:        fmt.Sprintf("%s/post/%s", o.BaseURL, postID),
			DownloadURL: l.URL,
			Description: post.Ficha.Descricao,
			Size:        post.FileSize,
			PubDate:     post.PublishedAt,
		})
	}

	return results, nil
}
