package configcache

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
)

// FileSource reads site configuration from a JSON file of the form
//
//	{"sites": [{"site": {...}, "experiments": [...]}]}
//
// It exists so the read path can be run and demonstrated without a
// database. Sites whose payload_version is zero get a version derived from
// the file contents, so editing the file changes the ETag.
type FileSource struct {
	Path string
}

type fileFormat struct {
	Sites []SiteConfig `json:"sites"`
}

// Load implements Source.
func (f FileSource) Load(_ context.Context) ([]SiteConfig, error) {
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var ff fileFormat
	if err := json.Unmarshal(raw, &ff); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", f.Path, err)
	}
	version := contentVersion(raw)
	for i := range ff.Sites {
		s := &ff.Sites[i]
		if s.Site.Key == "" {
			return nil, fmt.Errorf("config file %s: site %d has no key", f.Path, i)
		}
		if s.Site.Status == "" {
			s.Site.Status = "active"
		}
		if s.Site.PayloadVersion == 0 {
			s.Site.PayloadVersion = version
		}
		for j := range s.Experiments {
			for k := range s.Experiments[j].Variants {
				if s.Experiments[j].Variants[k].Source == "" {
					s.Experiments[j].Variants[k].Source = "manual"
				}
			}
		}
	}
	return ff.Sites, nil
}

// contentVersion hashes the file to a positive integer that fits in a
// JavaScript safe integer (53 bits).
func contentVersion(b []byte) int64 {
	h := fnv.New64a()
	h.Write(b)
	return int64(h.Sum64() & ((1 << 53) - 1))
}
