package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"torrProxy/api"
	"torrProxy/types"

	_ "github.com/joho/godotenv/autoload"
	_ "golang.org/x/crypto/x509roots/fallback"

	"go.uber.org/zap"
)

func init() {
	// Force pure Go DNS resolver (no CGO)
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = nil // Use default dialer
	// Global logger
	zapConfig := zap.NewDevelopmentConfig()
	encoder := zap.NewDevelopmentEncoderConfig()
	//zapConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	zapConfig.EncoderConfig = encoder
	zapConfig.Encoding = "console"
	zap.ReplaceGlobals(zap.Must(zapConfig.Build()))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", searchHandler)

	api.RegisterTorrProxyDownload(mux)

	addr := ":8090"
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	zap.L().Info(fmt.Sprintf("Listening on %s", addr), zap.Strings("indexers", listIndexerIds()))
	zap.L().Fatal("FATAL!!", zap.Error(srv.ListenAndServe()))
}

func listIndexerIds() []string {
	ids := make([]string, 0, len(types.Indexers))
	for _, idx := range types.Indexers {
		ids = append(ids, idx.Id())
	}
	return ids
}

// /search?q=ubuntu&indexers=Nyaa (rss),Mock
// If indexers param is omitted, search all indexers.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}
	indexerParam := r.URL.Query().Get("indexers")
	var toSearch []types.Indexer

	if indexerParam == "" {
		toSearch = types.Indexers
	} else {
		// comma-separated names
		requested := map[string]bool{}
		for _, nm := range strings.Split(indexerParam, ",") {
			requested[strings.TrimSpace(nm)] = true
		}
		for _, idx := range types.Indexers {
			if requested[idx.Id()] {
				toSearch = append(toSearch, idx)
			}
		}
		if len(toSearch) == 0 {
			http.Error(w, "no matching indexers found", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type backendResp struct {
		Indexer string
		Results []types.Result
		Error   string
	}
	ch := make(chan backendResp, len(toSearch))

	// query backends in parallel
	for _, idx := range toSearch {
		go func(idx types.Indexer) {
			results, err := idx.Search(ctx, q)
			br := backendResp{Indexer: idx.Name(), Results: results}
			if err != nil {
				br.Error = err.Error()
			}
			select {
			case ch <- br:
			case <-ctx.Done():
				// context done; try to send a failure record
				select {
				case ch <- backendResp{Indexer: idx.Name(), Error: ctx.Err().Error()}:
				default:
				}
			}
		}(idx)
	}

	// collect and flatten
	type FlatResult struct {
		types.Result
		Source string `json:"source,omitempty"`
	}
	flat := make([]FlatResult, 0)
	var errs []string
	for i := 0; i < len(toSearch); i++ {
		br := <-ch
		if br.Error != "" {
			errs = append(errs, br.Indexer+": "+br.Error)
			continue
		}
		for _, r := range br.Results {
			flat = append(flat, FlatResult{Result: r, Source: br.Indexer})
		}
	}

	// If everything failed, return an error
	if len(flat) == 0 && len(errs) > 0 {
		http.Error(w, "all backends failed: "+strings.Join(errs, " | "), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(flat)
}
