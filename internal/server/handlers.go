package server

import (
	"container/list"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go-vod-module/internal/config"
	"go-vod-module/internal/dash"
	"go-vod-module/internal/hls"
	"go-vod-module/internal/mp4"
)

//go:embed status.html
var statusHTML []byte

type Clip struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

type Sequence struct {
	Clips []Clip `json:"clips"`
}

type MappingJSON struct {
	Sequences []Sequence `json:"sequences"`
}

type cacheItem struct {
	key      string
	meta     *mp4.MovieMetadata
	segments []hls.HLSSegment
}

type LRUMetadataCache struct {
	maxEntries int
	evictList  *list.List
	items      map[string]*list.Element
	mutex      sync.Mutex
}

func NewLRUMetadataCache(maxEntries int) *LRUMetadataCache {
	return &LRUMetadataCache{
		maxEntries: maxEntries,
		evictList:  list.New(),
		items:      make(map[string]*list.Element),
	}
}

func (c *LRUMetadataCache) Get(key string) (*mp4.MovieMetadata, []hls.HLSSegment, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if ent, ok := c.items[key]; ok {
		c.evictList.MoveToFront(ent)
		item := ent.Value.(*cacheItem)
		return item.meta, item.segments, true
	}
	return nil, nil, false
}

func (c *LRUMetadataCache) Add(key string, meta *mp4.MovieMetadata, segments []hls.HLSSegment) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if ent, ok := c.items[key]; ok {
		c.evictList.MoveToFront(ent)
		item := ent.Value.(*cacheItem)
		item.meta = meta
		item.segments = segments
		return
	}

	item := &cacheItem{key: key, meta: meta, segments: segments}
	ent := c.evictList.PushFront(item)
	c.items[key] = ent

	if c.maxEntries > 0 && c.evictList.Len() > c.maxEntries {
		c.removeOldest()
	}
}

func (c *LRUMetadataCache) Remove(key string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if ent, ok := c.items[key]; ok {
		c.removeElement(ent)
		return true
	}
	return false
}

func (c *LRUMetadataCache) Purge() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.evictList = list.New()
	c.items = make(map[string]*list.Element)
}

func (c *LRUMetadataCache) Keys() []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	keys := make([]string, 0, len(c.items))
	for key := range c.items {
		keys = append(keys, key)
	}
	return keys
}

func (c *LRUMetadataCache) removeOldest() {
	ent := c.evictList.Back()
	if ent != nil {
		c.removeElement(ent)
	}
}

func (c *LRUMetadataCache) removeElement(e *list.Element) {
	c.evictList.Remove(e)
	item := e.Value.(*cacheItem)
	delete(c.items, item.key)
}

var (
	metadataCache = NewLRUMetadataCache(1000)
)

func getOrParseMetadata(cfg *config.Config, filePath string, item string) (*mp4.MovieMetadata, []hls.HLSSegment, error) {
	if meta, segments, ok := metadataCache.Get(filePath); ok {
		// log.Printf("[CACHE] HIT: %s (request: %s)", filePath, item)
		atomic.AddInt64(&TotalHits, 1)
		return meta, segments, nil
	}

	meta, err := mp4.Parse(filePath)
	if err != nil {
		return nil, nil, err
	}

	segments, err := hls.GenerateSegments(meta, cfg.DefaultSegmentDuration, cfg.AlignSegmentsToKeyFrames)
	if err != nil {
		return nil, nil, err
	}

	metadataCache.Add(filePath, meta, segments)

	// Calculate and log estimated memory usage
	totalSamples := 0
	for _, track := range meta.Tracks {
		totalSamples += len(track.Samples)
	}
	// estMemMB := float64(totalSamples*32) / (1024.0 * 1024.0)
	// log.Printf("[CACHE] MISS: %s (request: %s, parsed metadata, tracks: %d, samples: %d, estimated RAM: %.2f MB)", filePath, item, len(meta.Tracks), totalSamples, estMemMB)
	atomic.AddInt64(&TotalMisses, 1)

	return meta, segments, nil
}

func fetchMapping(upstreamURL, filename string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/%s", upstreamURL, filename))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream JSON server returned status: %d", resp.StatusCode)
	}

	var mapping MappingJSON
	if err := json.NewDecoder(resp.Body).Decode(&mapping); err != nil {
		return "", err
	}

	if len(mapping.Sequences) == 0 || len(mapping.Sequences[0].Clips) == 0 {
		return "", fmt.Errorf("invalid mapping: no sequences or clips found")
	}

	return mapping.Sequences[0].Clips[0].Path, nil
}

func resolveFilePath(cfg *config.Config, filename string) (string, error) {
	if cfg.Mode == "local" {
		cleanRoot := filepath.Clean(cfg.MediaRoot)
		joinedPath := filepath.Join(cleanRoot, filename)
		cleanPath := filepath.Clean(joinedPath)

		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil || strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
			return "", fmt.Errorf("access denied: path outside media root")
		}
		return cleanPath, nil
	}
	return fetchMapping(cfg.UpstreamJSONURL, filename)
}

func RegisterHandlers(mux *http.ServeMux, cfg *config.Config) {
	// Reinitialize cache with configured max entries
	metadataCache = NewLRUMetadataCache(cfg.MaxCacheEntries)

	// 0. Start background CSV stats history collector
	StartMetricCollector(cfg.MediaRoot)

	// Built-in status page dashboard (Auth: admin/admin)
	mux.HandleFunc("GET /status", BasicAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(statusHTML)
	}))

	// Built-in status JSON data endpoint (Auth: admin/admin)
	mux.HandleFunc("GET /status/data", BasicAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		history, err := ReadHistory()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"history": history,
		})
	}))

	// 1. Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})

	// Cache purge control route
	mux.HandleFunc("GET /control/purge-cache", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fileParam := r.URL.Query().Get("file")
		if fileParam != "" {
			resolved, err := resolveFilePath(cfg, fileParam)
			removed := false
			if err == nil {
				removed = metadataCache.Remove(resolved)
			}
			if !removed {
				removed = metadataCache.Remove(fileParam)
			}

			if removed {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"file %q purged from cache"}`, fileParam)))
			} else {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(fmt.Sprintf(`{"success":false,"message":"file %q not found in cache"}`, fileParam)))
			}
			return
		}

		metadataCache.Purge()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"message":"all cache purged"}`))
	})

	// Cache status list route
	mux.HandleFunc("GET /control/cache-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		keys := metadataCache.Keys()
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"cached_files": keys,
			"cache_count":  len(keys),
			"max_entries":  cfg.MaxCacheEntries,
		})
	})

	// 2. HLS route: /hls/{path...}
	hlsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS & Private Network Access headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Server,range,Content-Length,Content-Range")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		pathVal := r.PathValue("path")
		lastSlash := strings.LastIndex(pathVal, "/")
		if lastSlash == -1 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		filename := pathVal[:lastSlash]
		item := pathVal[lastSlash+1:]

		//log.Printf("[HLS] Request: %s (file: %s)", item, filename)

		// Resolve path to find absolute path
		filePath, err := resolveFilePath(cfg, filename)
		if err != nil {
			log.Printf("[HLS] Mapping error for %s: %v", filename, err)
			http.Error(w, fmt.Sprintf("Mapping failed: %v", err), http.StatusNotFound)
			return
		}

		meta, segments, err := getOrParseMetadata(cfg, filePath, item)
		if err != nil {
			log.Printf("[HLS] Metadata error for %s (%s): %v", filename, filePath, err)
			http.Error(w, fmt.Sprintf("Failed to parse metadata: %v", err), http.StatusInternalServerError)
			return
		}

		if item == "master.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(hls.GenerateMasterPlaylist(meta, "index-v1-a1.m3u8")))
			return
		}

		if item == "video.m3u8" || item == "index-v1-a1.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(hls.GenerateMediaPlaylist(segments, "seg-%d-v1-a1.ts")))
			return
		}

		// TS Segment requests (v-{num}.jpeg, 1-based index)
		if strings.HasPrefix(item, "v-") && strings.HasSuffix(item, ".jpeg") {
			segNumStr := strings.TrimPrefix(item, "v-")
			segNumStr = strings.TrimSuffix(segNumStr, ".jpeg")
			segNum1Based, err := strconv.Atoi(segNumStr)
			segNum := segNum1Based - 1
			if err != nil || segNum < 0 || segNum >= len(segments) {
				http.Error(w, "Segment not found", http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "video/mp2t")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = hls.MuxSegment(context.Background(), meta, segments[segNum], filePath, w)
			if err != nil {
				log.Printf("[HLS] TS Muxing error for segment %d of %s: %v", segNum, filePath, err)
			}
			return
		}

		// TS Segment requests (seg-{num}-v1-a1.ts, 1-based index with 0 fallback)
		if strings.HasPrefix(item, "seg-") && strings.HasSuffix(item, "-v1-a1.ts") {
			segNumStr := strings.TrimPrefix(item, "seg-")
			segNumStr = strings.TrimSuffix(segNumStr, "-v1-a1.ts")
			segNum1Based, err := strconv.Atoi(segNumStr)
			segNum := segNum1Based - 1
			if segNum1Based == 0 {
				segNum = 0
			}
			if err != nil || segNum < 0 || segNum >= len(segments) {
				http.Error(w, "Segment not found", http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "video/mp2t")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = hls.MuxSegment(context.Background(), meta, segments[segNum], filePath, w)
			if err != nil {
				log.Printf("[HLS] TS Muxing error for segment %d of %s: %v", segNum, filePath, err)
			}
			return
		}

		// fMP4 Init Segments (HLS-fMP4)
		if strings.HasPrefix(item, "init-") && strings.HasSuffix(item, ".mp4") {
			trackCode := strings.TrimPrefix(item, "init-")
			trackCode = strings.TrimSuffix(trackCode, ".mp4")
			trackType := "video"
			if trackCode == "a1" {
				trackType = "audio"
			}

			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = dash.MuxInitSegment(meta, trackType, w)
			if err != nil {
				log.Printf("[HLS-fMP4] Init segment error: %v", err)
			}
			return
		}

		// fMP4 Media Segments (HLS-fMP4)
		if strings.HasPrefix(item, "seg-") && strings.HasSuffix(item, ".m4s") {
			segNum, trackType, err := parseDASHSegInfo(item)
			// DASH segment numbers start at 1, map to index = segNum - 1
			segIdx := segNum - 1
			if err != nil || segIdx < 0 || segIdx >= len(segments) {
				http.Error(w, "Segment not found", http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "video/iso.segment")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = dash.MuxMediaSegment(context.Background(), meta, segments[segIdx], trackType, segNum, filePath, w)
			if err != nil {
				log.Printf("[HLS-fMP4] Media segment error for segment %d: %v", segNum, err)
			}
			return
		}

		http.Error(w, "Resource not found", http.StatusNotFound)
	})
	mux.HandleFunc("/hls/{path...}", MetricsMiddleware(hlsHandler, "/hls/*"))

	// 3. DASH route: /dash/{path...}
	dashHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS & Private Network Access headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Server,range,Content-Length,Content-Range")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		pathVal := r.PathValue("path")
		lastSlash := strings.LastIndex(pathVal, "/")
		if lastSlash == -1 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		filename := pathVal[:lastSlash]
		item := pathVal[lastSlash+1:]

		//log.Printf("[DASH] Request: %s (file: %s)", item, filename)

		filePath, err := resolveFilePath(cfg, filename)
		if err != nil {
			log.Printf("[DASH] Mapping error for %s: %v", filename, err)
			http.Error(w, fmt.Sprintf("Mapping failed: %v", err), http.StatusNotFound)
			return
		}

		meta, segments, err := getOrParseMetadata(cfg, filePath, item)
		if err != nil {
			log.Printf("[DASH] Metadata error for %s (%s): %v", filename, filePath, err)
			http.Error(w, fmt.Sprintf("Failed to parse metadata: %v", err), http.StatusInternalServerError)
			return
		}

		if item == "manifest.mpd" {
			manifestStr, err := dash.GenerateManifest(meta, segments)
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to generate manifest: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/dash+xml")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(manifestStr))
			return
		}

		// Init segment, e.g. "init-v1.mp4" or "init-a1.mp4"
		if strings.HasPrefix(item, "init-") && strings.HasSuffix(item, ".mp4") {
			trackCode := strings.TrimPrefix(item, "init-")
			trackCode = strings.TrimSuffix(trackCode, ".mp4")
			trackType := "video"
			if trackCode == "a1" {
				trackType = "audio"
			}

			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = dash.MuxInitSegment(meta, trackType, w)
			if err != nil {
				log.Printf("[DASH] Init segment error: %v", err)
			}
			return
		}

		// Media segments, e.g. "seg-2-v1.m4s"
		if strings.HasPrefix(item, "seg-") && strings.HasSuffix(item, ".m4s") {
			segNum, trackType, err := parseDASHSegInfo(item)
			segIdx := segNum - 1
			if err != nil || segIdx < 0 || segIdx >= len(segments) {
				http.Error(w, "Segment not found", http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "video/iso.segment")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)

			err = dash.MuxMediaSegment(context.Background(), meta, segments[segIdx], trackType, segNum, filePath, w)
			if err != nil {
				log.Printf("[DASH] Media segment error for segment %d: %v", segNum, err)
			}
			return
		}

		http.Error(w, "Resource not found", http.StatusNotFound)
	})
	mux.HandleFunc("/dash/{path...}", MetricsMiddleware(dashHandler, "/dash/*"))
}

func parseDASHSegInfo(name string) (int, string, error) {
	var num int
	var trackCode string
	_, err := fmt.Sscanf(name, "seg-%d-%2s.m4s", &num, &trackCode)
	if err != nil {
		return 0, "", err
	}
	trackType := "video"
	if trackCode == "a1" {
		trackType = "audio"
	}
	return num, trackType, nil
}
