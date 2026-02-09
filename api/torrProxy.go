package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"
	"torrProxy/indexers"
	"torrProxy/types"
)

// RegisterTorrProxyDownload registers the single download endpoint on the provided mux.
// Call this from your main (after mux is created).
func RegisterTorrProxyDownload(mux *http.ServeMux) {
	mux.HandleFunc("/torrproxy/download", torrProxyDownloadHandler)
}

func torrProxyDownloadHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	indexerParam := r.URL.Query().Get("indexer")
	dlURL := r.URL.Query().Get("dl_url")
	if indexerParam == "" || dlURL == "" {
		http.Error(w, "missing indexer or dl_url", http.StatusBadRequest)
		return
	}

	idx := types.FindIndexer(indexerParam)
	if idx == nil {
		http.Error(w, "indexer not found: "+indexerParam, http.StatusBadRequest)
		return
	}

	// Resolve details URL and choose client (use indexer's logged-in client when possible)
	client := http.DefaultClient
	baseURL := ""
	switch v := idx.(type) {
	case *indexers.AmigosShareIndexer:
		v.EnsureClient()
		if err := v.EnsureLogin(ctx); err != nil {
			http.Error(w, "amigosshare login failed: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if v.Client != nil {
			client = v.Client
		}
		baseURL = v.BaseURL
	case *indexers.RedeTorrent:
		if v.Client != nil {
			client = v.Client
		}
		baseURL = v.BaseURL
	case *indexers.CapybaraBRAPIIndexer:
		if v.Client != nil {
			client = v.Client
		}
		baseURL = v.BaseURL
	default:
		// fallback to default
	}

	// Ensure detailsURL absolute if possible
	u, err := neturl.Parse(dlURL)
	if err != nil || !u.IsAbs() {
		if baseURL != "" {
			if base, err := neturl.Parse(baseURL); err == nil {
				dlURL = base.ResolveReference(u).String()
			}
		}
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	req.Header.Set("User-Agent", "torrProxy/0.1")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch details page: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("details page returned %d: %s", resp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}

	// Fetch the torrent file and stream back using the indexer's client (so cookies preserved)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	req2.Header.Set("User-Agent", "torrProxy/0.1")
	resp2, err := client.Do(req2)
	if err != nil {
		http.Error(w, "failed to download torrent: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp2.Body.Close()
	if resp2.StatusCode >= 400 {
		b, _ := io.ReadAll(resp2.Body)
		http.Error(w, fmt.Sprintf("torrent download returned %d: %s", resp2.StatusCode, string(b)), http.StatusBadGateway)
		return
	}

	// Copy Content-Type (default to application/x-bittorrent)
	if ct := resp2.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/x-bittorrent")
	}
	if cd := resp2.Header.Get("Content-Disposition"); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp2.Body)
}
