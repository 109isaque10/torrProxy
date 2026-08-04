package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	switch v := idx.(type) {
	case *indexers.AmigosShareIndexer:
		v.EnsureClient()
		if v.Client != nil {
			client = v.Client
		}
	case *indexers.RedeTorrent:
		if v.Client != nil {
			client = v.Client
		}
	case *indexers.CapybaraBRAPIIndexer:
		if v.Client != nil {
			client = v.Client
		}
	default:
		// fallback to default
	}

	// Fetch the torrent file and stream back using the indexer's client (so cookies preserved)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	req.Header.Set("User-Agent", "torrProxy/1.0")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to download torrent: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("torrent download returned %d: %s", resp.StatusCode, string(b)), http.StatusBadGateway)
		return
	}

	// Copy Content-Type (default to application/x-bittorrent)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/x-bittorrent")
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}

	// w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}
