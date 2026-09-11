package types

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// Result represents one search result (torrent/nzb/etc).
type Result struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description,omitempty"`
	Free        bool      `json:"free,omitempty"`
	Size        string    `json:"size,omitempty"`
	PubDate     time.Time `json:"pubdate,omitzero"`
	Seeders     int       `json:"seeders,omitempty"`
	Leechers    int       `json:"leechers,omitempty"`
	InfoHash    string    `json:"infohash,omitempty"`
	DownloadURL string    `json:"download_url,omitempty"`
}

type Job struct {
	URL   string
	Title string
}

// Indexer is the interface all indexers implement.
type Indexer interface {
	Name() string
	Id() string
	IsEnabled() bool
	// Search performs a query (use ctx to set timeouts/cancellation).
	Search(ctx context.Context, query, alt string) ([]Result, error)
	Ping(ctx context.Context) bool
}

type AuthenticatedIndexer interface {
	EnsureLoggedIn() error
	SetAuth(bool)
}

var Indexers []Indexer

func ToString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON numbers decoded as float64
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// FindIndexer find an indexer by id (case-insensitive match on Id() or Name()).
func FindIndexer(idOrName string) Indexer {
	if idOrName == "" {
		return nil
	}
	lower := strings.ToLower(idOrName)
	for _, idx := range Indexers {
		// prefer ID if implemented
		if strings.ToLower(idx.Id()) == lower || strings.ToLower(idx.Name()) == lower {
			return idx
		}
	}
	return nil
}
