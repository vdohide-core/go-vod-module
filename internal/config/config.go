package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Port                   int    `json:"port"`
	Mode                   string `json:"mode"`       // "mapped" or "local"
	MediaRoot              string `json:"media_root"` // used if mode is "local"
	UpstreamJSONURL        string `json:"upstream_json_url"`
	DefaultSegmentDuration int    `json:"default_segment_duration"` // in milliseconds
	MaxCacheEntries        int    `json:"max_cache_entries"`        // max metadata entries to cache
	AlignSegmentsToKeyFrames bool   `json:"align_segments_to_key_frames"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}

	if cfg.Mode == "" {
		cfg.Mode = "mapped"
	}
	if cfg.Port == 0 {
		cfg.Port = 8889
	}
	if cfg.DefaultSegmentDuration == 0 {
		cfg.DefaultSegmentDuration = 4000
	}
	if cfg.MaxCacheEntries == 0 {
		cfg.MaxCacheEntries = 1000
	}

	return &cfg, nil
}
