package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-vod-module/internal/config"
	"go-vod-module/internal/mp4"
)

func TestHandlers(t *testing.T) {
	// 1. Create a mock JSON mapping server
	absPath, err := filepath.Abs("../../testdata/sample.mp4")
	if err != nil {
		t.Fatalf("Failed to get absolute path: %v", err)
	}

	mockJSONServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test.json" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		mapping := MappingJSON{
			Sequences: []Sequence{
				{
					Clips: []Clip{
						{
							Type: "source",
							Path: absPath,
						},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(mapping)
	}))
	defer mockJSONServer.Close()

	// 2. Setup server handlers
	cfg := &config.Config{
		Port:                   8889,
		UpstreamJSONURL:        mockJSONServer.URL,
		DefaultSegmentDuration: 4000,
	}

	mux := http.NewServeMux()
	RegisterHandlers(mux, cfg)

	// 3. Test Health Check
	req, _ := http.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected healthz code 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok\n" {
		t.Errorf("Expected healthz body 'ok\\n', got %q", rr.Body.String())
	}

	// 4. Test Master Playlist
	req, _ = http.NewRequest("GET", "/hls/test.json/master.m3u8", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected master.m3u8 code 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "#EXT-X-STREAM-INF") {
		t.Errorf("Expected master.m3u8 to contain EXT-X-STREAM-INF, got: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "index-v1-a1.m3u8") {
		t.Errorf("Expected master.m3u8 to contain index-v1-a1.m3u8, got: %q", rr.Body.String())
	}

	// 5. Test Media Playlist
	req, _ = http.NewRequest("GET", "/hls/test.json/index-v1-a1.m3u8", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected index-v1-a1.m3u8 code 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "#EXT-X-TARGETDURATION") {
		t.Errorf("Expected media playlist to contain target duration, got: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "seg-1-v1-a1.ts") {
		t.Errorf("Expected media playlist to contain seg-1-v1-a1.ts, got: %q", rr.Body.String())
	}

	// 6. Test TS Segment Muxing
	req, _ = http.NewRequest("GET", "/hls/test.json/seg-1-v1-a1.ts", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected seg-1-v1-a1.ts code 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}
	if len(rr.Body.Bytes()) == 0 {
		t.Error("Expected TS segment body to be non-empty")
	}

	// 7. Test DASH Manifest
	req, _ = http.NewRequest("GET", "/dash/test.json/manifest.mpd", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected manifest.mpd code 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "<MPD") {
		t.Errorf("Expected manifest.mpd to contain <MPD, got: %q", rr.Body.String())
	}

	// 8. Test DASH Init Segment (Video)
	req, _ = http.NewRequest("GET", "/dash/test.json/init-v1.mp4", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected init-v1.mp4 code 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}
	if len(rr.Body.Bytes()) == 0 {
		t.Error("Expected init-v1.mp4 body to be non-empty")
	}

	// 9. Test DASH Media Segment (Video)
	req, _ = http.NewRequest("GET", "/dash/test.json/seg-1-v1.m4s", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected seg-1-v1.m4s code 200, got %d. Body: %s", rr.Code, rr.Body.String())
	}
	if len(rr.Body.Bytes()) == 0 {
		t.Error("Expected seg-1-v1.m4s body to be non-empty")
	}
}

func TestCacheControl(t *testing.T) {
	// Create mock config with limit = 2
	cfg := &config.Config{
		Port:                   8889,
		DefaultSegmentDuration: 4000,
		MaxCacheEntries:        2,
	}

	mux := http.NewServeMux()
	RegisterHandlers(mux, cfg)

	// Since we don't parse real files here unless we mock them, we can test metadataCache directly or via endpoint
	meta1 := &mp4.MovieMetadata{}
	meta2 := &mp4.MovieMetadata{}
	meta3 := &mp4.MovieMetadata{}

	metadataCache.Add("file1", meta1, nil)
	metadataCache.Add("file2", meta2, nil)

	// verify they exist
	if _, _, ok := metadataCache.Get("file1"); !ok {
		t.Error("Expected file1 to be in cache")
	}
	if _, _, ok := metadataCache.Get("file2"); !ok {
		t.Error("Expected file2 to be in cache")
	}

	// Add 3rd item, should evict file1 (since file1 is oldest, and limit is 2)
	metadataCache.Add("file3", meta3, nil)

	if _, _, ok := metadataCache.Get("file1"); ok {
		t.Error("Expected file1 to be evicted from cache")
	}
	if _, _, ok := metadataCache.Get("file2"); !ok {
		t.Error("Expected file2 to be in cache")
	}
	if _, _, ok := metadataCache.Get("file3"); !ok {
		t.Error("Expected file3 to be in cache")
	}

	// Test GET /control/purge-cache?file=file2
	req, _ := http.NewRequest("GET", "/control/purge-cache?file=file2", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for file2 purge, got %d", rr.Code)
	}
	if _, _, ok := metadataCache.Get("file2"); ok {
		t.Error("Expected file2 to be purged from cache")
	}

	// Test GET /control/purge-cache (all)
	req, _ = http.NewRequest("GET", "/control/purge-cache", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for general purge, got %d", rr.Code)
	}
	if _, _, ok := metadataCache.Get("file3"); ok {
		t.Error("Expected file3 to be purged from cache")
	}
}
