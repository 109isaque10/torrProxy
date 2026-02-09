package indexers

// Converted & extended from capybarabr-api.yml (UNIT3D API).
// Config via env:
//  - CAPYBARA_APIKEY
//  - CAPYBARA_BASE (default https://capybarabr.com/)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"torrProxy/types"

	"github.com/coregx/coregex"
)

type CapybaraBRAPIIndexer struct {
	BaseURL   string
	APIKey    string
	Freeleech bool
	Client    *http.Client
}

func (c *CapybaraBRAPIIndexer) Name() string {
	return "CapybaraBR (API)"
}

func (c *CapybaraBRAPIIndexer) Id() string {
	return "capybarabr"
}

func (c *CapybaraBRAPIIndexer) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *CapybaraBRAPIIndexer) buildURL() (*neturl.URL, error) {
	u, err := neturl.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "api/torrents/filter")
	return u, nil
}

func intFromInterface(v interface{}) int {
	if v == nil {
		return 0
	}
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

func (c *CapybaraBRAPIIndexer) Search(ctx context.Context, query string) ([]types.Result, error) {
	m := coregex.MustCompile("complet")
	if m.MatchString(strings.ToLower(query)) {
		return nil, fmt.Errorf("no need to search for packs")
	}

	u, err := c.buildURL()
	if err != nil {
		return nil, err
	}

	qp := u.Query()
	qp.Set("name", query)
	qp.Set("perPage", "100")
	u.RawQuery = qp.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	req.Header.Set("User-Agent", "torrProxy/1.0")

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("capybarabr: bad response %d", resp.StatusCode)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	resultsRaw := payload["data"]
	if resultsRaw == nil {
		resultsRaw = payload["results"]
	}

	resultsSlice, _ := resultsRaw.([]interface{})
	if resultsSlice == nil {
		return nil, fmt.Errorf("capybarabr: unexpected json structure")
	}

	out := make([]types.Result, 0, len(resultsSlice))
	for _, ri := range resultsSlice {
		item, _ := ri.(map[string]interface{})
		if item == nil {
			continue
		}

		attrs := item
		if rawAttrs, ok := item["attributes"]; ok {
			if m, ok := rawAttrs.(map[string]interface{}); ok {
				attrs = m
			}
		}

		// free mapping (api returns false/true) -> map to numeric factor
		free := false
		if raw, ok := attrs["freeleech"]; ok {
			m := coregex.MustCompile("100[%]?")
			free = m.MatchString(types.ToString(raw))
		}
		if c.Freeleech && !free {
			return nil, fmt.Errorf("not free")
		}

		title := types.ToString(attrs["name"])
		download := types.ToString(attrs["download_link"])

		// parse created_at with timezone - YAML appended " -03:00" before parsing:
		createdAt := types.ToString(attrs["created_at"])
		if createdAt != "" && !strings.Contains(createdAt, "+") && !strings.Contains(createdAt, "-") {
			// append BRT offset if missing (YAML does this)
			createdAt = createdAt + " -03:00"
		}
		pub := ParseDateWithFormats(createdAt, []string{"01/02/2006 15:04:05 -07:00", time.RFC3339, "2006-01-02T15:04:05.000000Z"})

		res := types.Result{
			Title:      title,
			Link:       types.ToString(attrs["details_link"]),
			Size:       types.ToString(attrs["size"]),
			Free:       free,
			PubDate:    pub,
			Seeders:    toInt(attrs["seeders"]),
			Leechers:   toInt(attrs["leechers"]),
			InfoHash:   types.ToString(attrs["info_hash"]),
			TorrentURL: buildTorrProxyDownloadLink(c.Id(), download),
		}

		out = append(out, res)
	}

	return out, nil
}

func init() {
	base := defaultEnv("CAPYBARA_BASE", "https://capybarabr.com/")
	apiKey := os.Getenv("CAPYBARA_APIKEY")
	idx := &CapybaraBRAPIIndexer{
		BaseURL: base,
		APIKey:  apiKey,
	}
	types.Indexers = append(types.Indexers, idx)
}
