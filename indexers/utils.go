package indexers

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/wasilibs/go-re2"
	"go.uber.org/zap"

	"github.com/goccy/go-json"
)

var (
	cleanRe      = re2.MustCompile(`^(.*?)[\(](.*?)[\)](.*?)$`)
	langRe       = re2.MustCompile(`(?i)(Dual|Nacional|Dublado)`)
	dateRe       = re2.MustCompile(` (\d:)`)
	yearRe       = re2.MustCompile(`(19|20)\d{2}`)
	launchRe     = re2.MustCompile(`Lançado:\s*(.+)$`)
	completRe    = re2.MustCompile("complet")
	collectionRe = re2.MustCompile("collection")
	infoHashRe   = re2.MustCompile(`xt=urn:btih:([a-fA-F0-9]{40})`)
	magnetDnRe   = re2.MustCompile(`dn=([^&]+)`)
	seasonRe     = re2.MustCompile(`(?i)s0{1,2}?(\d{1,2})`)
)

//
// Helpers reused by indexers
//

// Helper to safely fetch meta tag attributes
func getMetaContent(doc *goquery.Document, selector string) string {
	val, _ := doc.Find(selector).Attr("content")
	return strings.TrimSpace(val)
}

// encodeISO88591 encodes spaces as '+' and non-ASCII chars like 'ª' as single-byte ISO hex (%AA)
func encodeISO88591(s string) string {
	var buf strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			buf.WriteString("+")
		case r == 'ª':
			buf.WriteString("%AA")
		case r == 'º':
			buf.WriteString("%BA")
		case r == 'ç':
			buf.WriteString("%E7")
		case r == 'Ç':
			buf.WriteString("%C7")
		case r == 'ã':
			buf.WriteString("%E3")
		case r < 128:
			buf.WriteRune(r)
		default:
			// Fallback for unexpected runes
			buf.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return buf.String()
}

func ExtractInfoHash(magnet string) string {
	// magnet:?xt=urn:btih:INFOHASH&dn=...
	matches := infoHashRe.FindStringSubmatch(magnet)
	if len(matches) > 1 {
		return strings.ToLower(matches[1])
	}
	return ""
}

func formatQuery(q string) string {
	// For TV shows, convert "S01E02" to "S0X02" to match site format
	q = strings.ToLower(q)
	q = strings.ReplaceAll(q, " complet", "")
	q = seasonRe.ReplaceAllString(q, "")
	return q
}

func keywordPreprocess(q string) (string, string) {
	s := strings.ToLower(q)
	year := ""
	if yearRe.MatchString(s) {
		year = yearRe.FindString(s)
		s = strings.TrimSpace(yearRe.ReplaceAllString(s, ""))
	}
	s = strings.ReplaceAll(s, " complet", "")
	// s0(\d{1,2})$ -> temporada $1
	s = seasonRe.ReplaceAllString(s, "${1}ª temporada")
	return s, year
}

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
	re := re2.MustCompile(`\d+`)
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
	u, err := url.Parse(defaultEnv("EXTERNAL_URL", "http://127.0.0.1:8090"))
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

func cleanTitle(title, year, quality, language string) string {
	// Strip non-english title and keep english between parentheses
	if m := cleanRe.FindStringSubmatch(title); len(m) == 4 {
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

	return langRe.ReplaceAllString(title, "Brazilian $1")
}

func extractDate(s *goquery.Selection) string {
	dateText := ""
	s.Find("p").EachWithBreak(func(i int, p *goquery.Selection) bool {
		txt := strings.TrimSpace(p.Text())
		if strings.Contains(txt, "Lançado:") {
			if m := launchRe.FindStringSubmatch(txt); len(m) == 2 {
				dateText = strings.TrimSpace(m[1])
			}
			return false
		}
		return true
	})
	if dateText != "" {
		dateText = dateRe.ReplaceAllString(dateText, " 0$1")
	}
	return dateText
}

func toInt(v any) int {
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

func defaultEnv(key, def string) string {
	if v := getEnv(key); v != "" {
		return v
	}
	return def
}

func getEnv(key string) string {
	return strings.Trim(strings.TrimSpace(os.Getenv(key)), `"'`)
}

// CleanAndCutTitle strips everything from s01/complete/1080p onwards and normalizes spaces
func CleanAndCutTitle(rawTitle string) string {
	lower := strings.ToLower(rawTitle)
	// Replace common delimiters with spaces
	cleaned := strings.NewReplacer("-", " ", "(", " ", ")", " ", ".", " ", "_", " ", "-", " ").Replace(lower)

	// Truncate at the first occurrence of s01, season, complete, etc.
	if idx := strings.Index(cleaned, " s0"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	if idx := strings.Index(cleaned, " season"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	if idx := strings.Index(cleaned, " temporada"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	if idx := strings.Index(cleaned, " complet"); idx != -1 {
		cleaned = cleaned[:idx]
	}

	return strings.Join(strings.Fields(cleaned), " ")
}

// IsValidPrefix checks if the cleaned title starts with or contains the search query
func IsValidPrefix(query, year, rawTitle string) bool {
	cleanQ := CleanAndCutTitle(query)
	cleanT := CleanAndCutTitle(rawTitle)
	yearBool := true
	if year != "" && yearRe.MatchString(cleanT) {
		yearMatch, _ := strconv.Atoi(yearRe.FindString(cleanT))
		yearInt, _ := strconv.Atoi(year)
		yearBool = yearInt == yearMatch || yearInt == yearMatch-1 || yearInt == yearMatch+1
	}
	bool := strings.Contains(cleanT, cleanQ) && yearBool
	return bool
}
