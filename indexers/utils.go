package indexers

import (
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/coregx/coregex"
	"go.uber.org/zap"

	"github.com/goccy/go-json"
)

//
// Helpers reused by indexers
//

// AbsURL resolves href (which may be relative) against base.
func AbsURL(base, href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	u, err := url.Parse(base)
	if err != nil {
		return href
	}
	rel, err := url.Parse(href)
	if err != nil {
		return href
	}
	return u.ResolveReference(rel).String()
}

// ParseIntFromText extracts digits and returns int (0 on failure).
func ParseIntFromText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	re := coregex.MustCompile(`\d+`)
	m := re.FindString(s)
	if m == "" {
		return 0
	}
	i, _ := strconv.Atoi(m)
	return i
}

// ParseDateWithFormats attempts several layouts returning zero time on failure.
func ParseDateWithFormats(s string, layouts []string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	// Try RFC parsing via json unmarshal (some APIs return json date)
	var try string
	if err := json.Unmarshal([]byte(s), &try); err == nil {
		for _, l := range layouts {
			if t, err := time.Parse(l, try); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func buildTorrProxyDownloadLink(indexerID, dlURL string) string {
	u, err := url.Parse("http://localhost:8090")
	if err != nil {
		zap.L().Error("Error on Parse Neturl", zap.Error(err))
		return ""
	}
	// Ensure path join is correct
	u.Path = path.Join(u.Path, "torrproxy", "download")
	q := u.Query()
	q.Set("indexer", indexerID)
	q.Set("dl_url", dlURL)
	u.RawQuery = q.Encode()
	return u.String()
}

func defaultEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func cleanTitle(title, year, quality, language string) string {
	// Strip non-english title and keep english between parentheses
	if m := coregex.MustCompile(`^(.*?)[\(](.*?)[\)](.*?)$`).FindStringSubmatch(title); len(m) == 4 {
		title = strings.TrimSpace(m[2] + m[3])
	}

	if year != "" {
		title += " " + year
	}
	if quality != "" {
		if strings.EqualFold(quality, "4k") {
			quality = "2160p"
		}
		title += " " + quality
	}
	if language != "" {
		title += " " + language
	}

	return coregex.MustCompile(`(?i)(Dual|Nacional|Dublado)`).ReplaceAllString(title, "Brazilian $1")
}

func extractDate(s *goquery.Selection) string {
	dateText := ""
	s.Find("p").EachWithBreak(func(i int, p *goquery.Selection) bool {
		txt := strings.TrimSpace(p.Text())
		if strings.Contains(txt, "Lançado:") {
			if m := coregex.MustCompile(`Lançado:\s*(.+)$`).FindStringSubmatch(txt); len(m) == 2 {
				dateText = strings.TrimSpace(m[1])
			}
			return false
		}
		return true
	})
	if dateText != "" {
		dateText = coregex.MustCompile(` (\d:)`).ReplaceAllString(dateText, " 0$1")
	}
	return dateText
}

func toInt(v interface{}) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if i, err := strconv.Atoi(x); err == nil {
			return i
		}
	}
	return 0
}
