package indexers

// Converted & extended from torrent-yml
// Config from environment:
//  - REDE_TORRENT_BASE (default: http://192.168.1.179:4949)
// This indexer calls /indexers/rede_torrent and expects JSON with results array.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"strings"
	"time"
	"torrProxy/types"

	"github.com/coregx/coregex"
)

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
	u.Path = path.Join(u.Path, "indexers/rede_torrent")
	return u.String(), nil
}

// keywordPreprocess performs the YAML filters: tolower, replace " complet" -> "", season S0/S -> "temporada X"
func (r *RedeTorrent) keywordPreprocess(q string) string {
	s := strings.ToLower(q)
	s = strings.ReplaceAll(s, " complet", "")
	// S0(\d{1,2})$ -> temporada $2
	re1 := coregex.MustCompile(`(?i)(S0)(\d{1,2})$`)
	s = re1.ReplaceAllString(s, "temporada $2")
	re2 := coregex.MustCompile(`(?i)(S)(\d{1,3})$`)
	s = re2.ReplaceAllString(s, "temporada $2")
	return s
}

func (r *RedeTorrent) Search(ctx context.Context, query string) ([]types.Result, error) {
	url, err := r.buildURL()
	if err != nil {
		return nil, err
	}
	u, _ := neturl.Parse(url)

	qp := u.Query()
	qp.Set("q", r.keywordPreprocess(query))
	qp.Set("filter_results", "true")
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

	var payload map[string]interface{}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}

	resultsRaw := payload["results"]
	if resultsRaw == nil {
		resultsRaw = payload["data"]
	}

	resultsSlice, ok := resultsRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("redetorrent: unexpected results format")
	}

	out := make([]types.Result, 0, len(resultsSlice))
	for _, ri := range resultsSlice {
		item, _ := ri.(map[string]interface{})
		if item == nil {
			continue
		}
		magnet := types.ToString(item["magnet_link"])
		if magnet == "" {
			magnet = types.ToString(item["magnet"])
		}
		title := strings.TrimSpace(types.ToString(item["title"]))
		if title == "" {
			title = strings.TrimSpace(types.ToString(item["original_title"]))
		}
		infohash := types.ToString(item["info_hash"])
		date := types.ToString(item["date"])
		size := types.ToString(item["size"])
		seeders := 0
		if s := item["seed_count"]; s != nil {
			s = toInt(s)
		}
		leechers := 0
		if l := item["leech_count"]; l != nil {
			l = toInt(l)
		}

		pub := ParseDateWithFormats(date, []string{time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05", "02/01/2006 15:04:05"})

		res := types.Result{
			Title:       title,
			Link:        types.ToString(item["details"]),
			Description: types.ToString(item["original_title"]),
			Size:        size,
			PubDate:     pub,
			Seeders:     seeders,
			Leechers:    leechers,
			InfoHash:    infohash,
			TorrentURL:  magnet,
		}

		out = append(out, res)
	}

	return out, nil
}

func init() {
	base := os.Getenv("REDE_TORRENT_BASE")
	if base == "" {
		base = "http://127.0.0.1:4949"
	}
	idx := &RedeTorrent{
		BaseURL: base,
	}
	types.Indexers = append(types.Indexers, idx)
}
