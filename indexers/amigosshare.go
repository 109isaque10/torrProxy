package indexers

// AmigosShareIndexer's improved login flow (updated to detect site-specific alerts
// and meta-refresh redirects that indicated the login failed in your HTML dumps).
//
// Key changes:
// - After POST, look for any `.alert` elements (not only `div.alert-error`) and return their text.
// - After the check GET, treat meta-refresh back to account-login.php or the presence of the login form
//   as "not logged in" and return a clear error.

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
	"time"
	"torrProxy/types"

	"github.com/coregx/coregex"

	"github.com/PuerkitoBio/goquery"
)

type AmigosShareIndexer struct {
	BaseURL   string
	Client    *http.Client
	Username  string
	Password  string
	Freeleech bool
	Sort      string
	Order     string

	mu              sync.RWMutex
	lastLoginCheck  time.Time
	loginCheckValid time.Duration // How long to trust the login state
	isCurrentlyLoggedIn bool
}

func (a *AmigosShareIndexer) Name() string {
	return "Amigos Share Club (ASC)"
}

func (a *AmigosShareIndexer) Id() string {
	return "amigosshare"
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
	// Initialize login check validity period (5 minutes default)
	if a.loginCheckValid == 0 {
		a.loginCheckValid = 10 * time.Minute
	}
}

func (a *AmigosShareIndexer) EnsureLogin(ctx context.Context) error {
	if a.Username == "" || a.Password == "" {
		return nil
	}
	
	a.EnsureClient()
	
	// Check cached login state first
	a.mu.RLock()
	if a.isCurrentlyLoggedIn && time.Since(a.lastLoginCheck) < a.loginCheckValid {
		a.mu.RUnlock()
		return nil // Still logged in based on cache
	}
	a.mu.RUnlock()
	
	// Need to verify login status
	loggedIn, err := a.isLoggedIn(ctx)
	if err != nil {
		return err
	}
	
	if loggedIn {
		// Update cache
		a.mu.Lock()
		a.isCurrentlyLoggedIn = true
		a.lastLoginCheck = time.Now()
		a.mu.Unlock()
		return nil
	}
	
	// Not logged in, attempt login
	if err := a.login(ctx); err != nil {
		// Clear cache on login failure
		a.mu.Lock()
		a.isCurrentlyLoggedIn = false
		a.mu.Unlock()
		return err
	}
	
	// Update cache after successful login
	a.mu.Lock()
	a.isCurrentlyLoggedIn = true
	a.lastLoginCheck = time.Now()
	a.mu.Unlock()
	
	return nil
}

func (a *AmigosShareIndexer) GetClient() *http.Client {
	return a.Client
}

func (a *AmigosShareIndexer) GetBaseURL() string {
	return a.BaseURL
}

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
func (a *AmigosShareIndexer) isLoggedIn(ctx context.Context) (bool, error) {
	if a.Username == "" || a.Password == "" {
		return true, nil // No credentials, consider as "logged in" (no auth needed)
	}
	a.EnsureClient()

	a.EnsureClient()

	checkURL, err := neturl.Parse(a.BaseURL)
	if err != nil {
		return false, err
	}
	checkURL.Path = path.Join(checkURL.Path, "torrents-search.php")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "torrProxy/0.1")
	resp, err := a.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	checkBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	bodyStr := string(checkBody)
	// If we see logout link or no login form, we're logged in
	hasLogout := strings.Contains(bodyStr, "account-logout.php") || 
	             strings.Contains(bodyStr, "logout") || 
	             strings.Contains(bodyStr, "Sair")
	hasLoginForm := strings.Contains(bodyStr, "account-login.php")

	// Logged in if we have logout link and no login form
	return hasLogout && !hasLoginForm, nil
}

// login posts the login form and verifies login.
func (a *AmigosShareIndexer) login(ctx context.Context) error {
	if a.Username == "" || a.Password == "" {
		return nil
	}
	a.EnsureClient()

	// 1) GET login page to collect cookies and hidden inputs
	loginURL := a.resolveAction("account-login.php")
	reqGet, _ := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	reqGet.Header.Set("User-Agent", "jackett-lite/0.1")
	respGet, err := a.Client.Do(reqGet)
	if err != nil {
		return fmt.Errorf("amigosshare: GET login page failed: %w", err)
	}
	defer respGet.Body.Close()

	getBody, _ := io.ReadAll(respGet.Body)
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(string(getBody)))

	// Collect form values
	formValues := neturl.Values{}
	if doc != nil {
		doc.Find("form input[name]").Each(func(i int, in *goquery.Selection) {
			if name, ok := in.Attr("name"); ok {
				val, _ := in.Attr("value")
				formValues.Set(name, val)
			}
		})
	}

	// Ensure required fields are set according to YAML: username, password, autologout
	formValues.Set("username", a.Username)
	formValues.Set("password", a.Password)
	formValues.Set("autologout", "yes")

	// POST login
	reqPost, _ := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(formValues.Encode()))
	reqPost.Header.Set("User-Agent", "torrProxy/0.1")
	reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqPost.Header.Set("Referer", loginURL)

	respPost, err := a.Client.Do(reqPost)
	if err != nil {
		return fmt.Errorf("POST login failed: %w", err)
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
			return errors.New("login error: " + msg)
		}
	}

	// Verify login
	checkURL, _ := neturl.Parse(a.BaseURL)
	checkURL.Path = path.Join(checkURL.Path, "torrents-search.php")

	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, checkURL.String(), nil)
	req2.Header.Set("User-Agent", "torrProxy/0.1")
	resp2, err := a.Client.Do(req2)
	if err != nil {
		return fmt.Errorf("GET check page failed: %w", err)
	}
	defer resp2.Body.Close()

	checkBody, _ := io.ReadAll(resp2.Body)
	checkStr := strings.ToLower(string(checkBody))

	// Check for meta refresh to login
	if strings.Contains(checkStr, "account-login.php") && strings.Contains(checkStr, "refresh") {
		return errors.New("login failed (redirected to login page)")
	}

	// Check for logout link
	if doc3, err := goquery.NewDocumentFromReader(strings.NewReader(string(checkBody))); err == nil {
		foundLogout := false
		doc3.Find("a").EachWithBreak(func(i int, s *goquery.Selection) bool {
			if href, ok := s.Attr("href"); ok {
				if strings.Contains(href, "account-logout.php") {
					foundLogout = true
					return false
				}
			}
			t := strings.ToLower(strings.TrimSpace(s.Text()))
			if strings.Contains(t, "logout") || strings.Contains(t, "sair") {
				foundLogout = true
				return false
			}
			return true
		})
		if !foundLogout {
			return errors.New("login failed (logout link not found)")
		}
	}

	return nil
}

// buildSearchURL builds torrents-search.php query URL from YAML mapping.
func (a *AmigosShareIndexer) buildSearchURL(query string) (string, error) {
	q := coregex.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(query), "%") // spaces -> %
	u, err := neturl.Parse(a.BaseURL)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "torrents-search.php")
	vals := neturl.Values{}
	vals.Set("search", q)
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

func (a *AmigosShareIndexer) Search(ctx context.Context, query string) ([]types.Result, error) {
	a.EnsureClient()
	// login if credentials provided
	if err := a.login(ctx); err != nil {
		// return the login error so the caller knows (and can inspect/adjust creds)
		return nil, err
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
			TorrentURL:  buildTorrProxyDownloadLink(a.Id(), AbsURL(a.BaseURL, downloadHref)),
		}

		if downloadVol == 0.0 {
			res.Free = true
		}

		out = append(out, res)
	})

	return out, nil

}

func init() {
	idx := &AmigosShareIndexer{
		BaseURL:  defaultEnv("AMIGOS_BASE", "https://cliente.amigos-share.club/"),
		Username: defaultEnv("AMIGOS_USERNAME", ""),
		Password: defaultEnv("AMIGOS_PASSWORD", ""),
		Freeleech: func() bool {
			v, _ := strconv.ParseBool(defaultEnv("AMIGOS_FREELECH", "false"))
			return v
		}(),
		Sort:  defaultEnv("AMIGOS_SORT", "id"),
		Order: defaultEnv("AMIGOS_ORDER", "desc"),
	}
	// ensure we have client with cookiejar
	idx.Client = newAmigosClient()

	types.Indexers = append(types.Indexers, idx)
}
