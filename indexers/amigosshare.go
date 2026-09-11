package indexers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"torrProxy/caching"
	"torrProxy/types"

	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"

	"github.com/PuerkitoBio/goquery"
)

type AmigosShareIndexer struct {
	BaseIndexer

	Username  string
	Password  string
	Freeleech bool
	Sort      string
	Order     string

	loginOnce sync.Once
	loginErr  error

	cache *ttlcache.Cache[string, any]
}

func (a *AmigosShareIndexer) Name() string {
	return "Amigos Share Club (ASC)"
}

func (a *AmigosShareIndexer) Id() string {
	return "amigosshare"
}

func (a *AmigosShareIndexer) IsEnabled() bool {
	return a.BaseIndexer.IsEnabled()
}

func (a *AmigosShareIndexer) SetAuth(e bool) {
	a.BaseIndexer.IsAuthenticated.Store(e)
}

func (a *AmigosShareIndexer) Ping(ctx context.Context) bool {
	return a.BaseIndexer.Ping(ctx, a.Name())
}

func newAmigosClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
	}
}

func (a *AmigosShareIndexer) EnsureClient() {
	if a.Client == nil {
		a.Client = newAmigosClient()
	}
}

//func (a *AmigosShareIndexer) EnsureLogin(ctx context.Context) error {
//	if a.Username == "" || a.Password == "" {
//		return nil
//	}
//
//	a.EnsureClient()
//
//	// Check cached login state first
//	a.mu.RLock()
//	if a.isCurrentlyLoggedIn && time.Since(a.lastLoginCheck) < a.loginCheckValid {
//		a.mu.RUnlock()
//		return nil // Still logged in based on cache
//	}
//	a.mu.RUnlock()
//
//	// Need to verify login status
//	loggedIn, err := a.isLoggedIn(ctx)
//	if err != nil {
//		return err
//	}
//
//	if loggedIn {
//		// Update cache
//		a.mu.Lock()
//		a.isCurrentlyLoggedIn = true
//		a.lastLoginCheck = time.Now()
//		a.mu.Unlock()
//		return nil
//	}
//
//	// Not logged in, attempt login
//	if err := a.login(ctx); err != nil {
//		// Clear cache on login failure
//		a.mu.Lock()
//		a.isCurrentlyLoggedIn = false
//		a.mu.Unlock()
//		return err
//	}
//
//	// Update cache after successful login
//	a.mu.Lock()
//	a.isCurrentlyLoggedIn = true
//	a.lastLoginCheck = time.Now()
//	a.mu.Unlock()
//
//	return nil
//}

// helper: resolve possibly relative action against BaseURL
func (a *AmigosShareIndexer) resolveAction(action string) string {
	if action == "" {
		return a.BaseURL
	}
	if strings.HasPrefix(action, "http://") || strings.HasPrefix(action, "https://") {
		return action
	}
	base, err := neturl.Parse(a.BaseURL)
	if err != nil {
		return action
	}
	rel, err := neturl.Parse(action)
	if err != nil {
		return action
	}
	return base.ResolveReference(rel).String()
}

// isLoggedIn checks if the user is currently logged in by checking for logout link
//func (a *AmigosShareIndexer) isLoggedIn(ctx context.Context) (bool, error) {
//	if a.Username == "" || a.Password == "" {
//		return true, nil // No credentials, consider as "logged in" (no auth needed)
//	}
//	a.EnsureClient()
//
//	a.EnsureClient()
//
//	checkURL, err := neturl.Parse(a.BaseURL)
//	if err != nil {
//		return false, err
//	}
//	checkURL.Path = path.Join(checkURL.Path, "torrents-search.php")
//
//	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL.String(), nil)
//	if err != nil {
//		return false, err
//	}
//	req.Header.Set("User-Agent", "torrProxy/1.0")
//	resp, err := a.Client.Do(req)
//	if err != nil {
//		return false, err
//	}
//	defer resp.Body.Close()
//
//	checkBody, err := io.ReadAll(resp.Body)
//	if err != nil {
//		return false, err
//	}
//
//	bodyStr := string(checkBody)
//	// If we see logout link or no login form, we're logged in
//	hasLogout := strings.Contains(bodyStr, "account-logout.php") ||
//	             strings.Contains(bodyStr, "logout") ||
//	             strings.Contains(bodyStr, "Sair")
//	hasLoginForm := strings.Contains(bodyStr, "account-login.php")
//
//	// Logged in if we have logout link and no login form
//	return hasLogout && !hasLoginForm, nil
//}

// login posts the login form and verifies login.
func (a *AmigosShareIndexer) EnsureLoggedIn() error {
	if a.Username == "" || a.Password == "" {
		return nil
	}

	a.EnsureClient()

	a.loginOnce.Do(func() {
		authCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		loginURL := a.resolveAction("account-login.php")

		// Ensure required fields are set according to YAML: username, password, autologout
		formValues := neturl.Values{}
		formValues.Set("username", a.Username)
		formValues.Set("password", a.Password)
		formValues.Set("autologout", "yes")

		// POST login
		reqPost, _ := http.NewRequestWithContext(authCtx, http.MethodPost, loginURL, strings.NewReader(formValues.Encode()))
		reqPost.Header.Set("User-Agent", "torrProxy/1.0")
		reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		reqPost.Header.Set("Referer", loginURL)

		respPost, err := a.Client.Do(reqPost)
		if err != nil {
			a.loginErr = fmt.Errorf("POST login failed: %w", err)
			return
		}
		defer respPost.Body.Close()

		postBody, _ := io.ReadAll(respPost.Body)

		// Check for error alerts
		if doc2, err := goquery.NewDocumentFromReader(strings.NewReader(string(postBody))); err == nil {
			if sel := doc2.Find(".alert"); sel.Length() > 0 {
				msg := strings.TrimSpace(sel.First().Text())
				if msg == "" {
					msg = "login failed: server returned alert"
				}
				a.loginErr = errors.New("login error: " + msg)
				return
			}
		}

		zap.L().Info("✅ AmigosShare authentication successful")
	})

	return a.loginErr
}

// buildSearchURL builds torrents-search.php query URL from YAML mapping.
func (a *AmigosShareIndexer) buildSearchURL(query string) (string, error) {
	u, err := neturl.Parse(a.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "torrents-search.php")
	vals := neturl.Values{}
	vals.Set("search", query)
	vals.Set("tipo", "precisa")
	if a.Sort != "" {
		vals.Set("sort", a.Sort)
	} else {
		vals.Set("sort", "id")
	}
	if a.Order != "" {
		vals.Set("order", a.Order)
	} else {
		vals.Set("order", "desc")
	}
	u.RawQuery = vals.Encode()
	return u.String(), nil
}

func (a *AmigosShareIndexer) Search(ctx context.Context, query, alt string) ([]types.Result, error) {
	query = strings.ToLower(query)
	if collectionRe.MatchString(query) {
		query = collectionRe.ReplaceAllString(query, "coleção")
	}
	queryYear := ""
	if yearRe.MatchString(query) {
		queryYear = yearRe.FindString(query)
		query = strings.TrimSpace(yearRe.ReplaceAllString(query, ""))
	}

	a.EnsureClient()

	// Check cache first if cache is available
	if a.cache != nil {
		cacheKey := caching.GenerateCacheKey(a.Id(), query)
		if cached := a.cache.Get(cacheKey); cached != nil {
			if results, ok := cached.Value().([]types.Result); ok {
				zap.L().Debug("📦 Cache hit for amigosshare search", zap.String("query", query))
				return results, nil
			}
		}
	}

	url, err := a.buildSearchURL(query)
	if err != nil {
		return nil, err
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("amigosshare: bad response %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	out := make([]types.Result, 0)
	selector := "div#fancy-list-group ul.list-group li.list-group-item"

	cleanQ := CleanAndCutTitle(query)
	doc.Find(selector).Each(func(i int, s *goquery.Selection) {
		// Freeleech filter: YAML used :has(span.badge-success:contains("FREE"))
		if a.Freeleech && s.Find("span.badge-success:contains('FREE')").Length() == 0 {
			return
		}

		titleSel := s.Find(`a[href*="torrents-details.php?id="], a[href*="details-misc.php?id="]`).First()
		title := strings.TrimSpace(titleSel.Text())
		detailsHref, _ := titleSel.Attr("href")
		downloadHref, _ := s.Find(`a[href*="download.php?id="]`).First().Attr("href")
		size := strings.TrimSpace(s.Find("div.list-group-item-content p.m-0 span.badge-info").First().Text())
		seeders := ParseIntFromText(s.Find("div.list-group-item-controls a").Eq(0).Text())
		leechers := ParseIntFromText(s.Find("div.list-group-item-controls a").Eq(1).Text())

		// genre (badge)
		genre := strings.TrimSpace(s.Find(`div.list-group-item-content p.m-0 span.badge-primary[style$="#1c38c2;"]`).Text())

		// _year / _quality / _type / _language extraction (simplified)
		quality := strings.TrimSpace(s.Find(`div.list-group-item-content p.m-0 span.badge-primary:contains("1080p"), div.list-group-item-content p.m-0 span.badge-primary:contains("720p"), div.list-group-item-content p.m-0 span.badge-primary:contains("4k")`).First().Text())
		year := strings.TrimSpace(s.Find(`div.list-group-item-content p.m-0 span.badge-primary[style$="#246AB6;"]`).First().Text())
		language := strings.TrimSpace(s.Find(`div.list-group-item-content p.m-0 span.badge-primary[style$="#b6249d;"]`).First().Text())

		// Title filters
		title = cleanTitle(title, year, quality, language)

		// Perform title validation checks
		if !IsValidPrefix(cleanQ, queryYear, title) || !seasonRe.MatchString(query) && seasonRe.MatchString(title) {
			return
		}

		downloadVol := 1.0
		if s.Find(`span.badge-success:contains("FREE")`).Length() > 0 {
			downloadVol = 0.0
		}

		// date extraction
		dateText := extractDate(s)
		pubDate := ParseDateWithFormats(dateText, []string{"02/01/06 15:04:05", "02/01/2006 15:04:05", time.RFC3339})

		res := types.Result{
			Title:       title,
			Link:        AbsURL(a.BaseURL, detailsHref),
			Description: genre,
			Size:        size,
			PubDate:     pubDate,
			Seeders:     seeders,
			Leechers:    leechers,
			InfoHash:    "",
			DownloadURL: buildTorrProxyDownloadLink(a.Id(), AbsURL(a.BaseURL, downloadHref)),
		}

		if downloadVol == 0.0 {
			res.Free = true
		}

		out = append(out, res)
	})

	if len(out) == 0 && strings.Contains(query, "complet") {
		out, err = a.Search(ctx, strings.ReplaceAll(query, " complet", ""), "")
		if err != nil {
			return nil, err
		}
	}

	// Cache the results if cache is available
	if a.cache != nil {
		cacheKey := caching.GenerateCacheKey(a.Id(), query)
		a.cache.Set(cacheKey, out, 2*time.Hour)
		zap.L().Debug("💾 Cached amigosshare search results", zap.String("query", query), zap.String("key", cacheKey), zap.Duration("ttl", 2*time.Hour), zap.Int("count", len(out)))
	}

	return out, nil

}

func init() {
	idx := &AmigosShareIndexer{
		BaseIndexer: BaseIndexer{
			BaseURL: defaultEnv("AMIGOS_BASE", "https://cliente.amigos-share.club/"),
		},
		Username: defaultEnv("AMIGOS_USERNAME", ""),
		Password: defaultEnv("AMIGOS_PASSWORD", ""),
		Freeleech: func() bool {
			v, _ := strconv.ParseBool(defaultEnv("AMIGOS_FREELECH", "false"))
			return v
		}(),
		Sort:  defaultEnv("AMIGOS_SORT", "id"),
		Order: defaultEnv("AMIGOS_ORDER", "desc"),
		cache: caching.C().Cache,
	}
	if idx.Username == "" || idx.Password == "" {
		return
	}

	idx.IsAlive.Store(true)
	idx.IsAuthenticated.Store(true) // Checks auth after

	// ensure we have client with cookiejar
	idx.Client = newAmigosClient()

	types.Indexers = append(types.Indexers, idx)
}
