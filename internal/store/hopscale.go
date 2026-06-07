// HopScaling + TrafficManagement live state, parsed from the firmware debug
// log (the modules are not exposed as telemetry). Populated by app.go when a
// LogRecord event arrives and fwparser recognises a "[HOPSCALE]" / "[TM]" line.

package store

import (
	"time"

	"mesh-reader/internal/fwparser"
)

// HopScaleStatus is the JSON-friendly snapshot of the HopScaling and
// TrafficManagement modules. Enabled is false until we've parsed at least one
// line; UpdatedAt / TMCacheAt gate the two sub-sections independently.
type HopScaleStatus struct {
	Enabled   bool  `json:"enabled"`
	UpdatedAt int64 `json:"updated_at"` // last "[HOPSCALE]" stats/perHop line

	// Hop-scaling state (from the "[HOPSCALE] hop=..." line).
	Hop         int `json:"hop"`
	HistActive  int `json:"hist_active"`
	FillPct     int `json:"fill_pct"`
	SampNum     int `json:"samp_num"`
	SampDen     int `json:"samp_den"`
	FiltNum     int `json:"filt_num"`
	FiltDen     int `json:"filt_den"`
	Entries     int `json:"entries"`
	LastCounted int `json:"last_counted"`
	PoliteNum   int `json:"polite_num"`
	PoliteDen   int `json:"polite_den"`
	NextRollMin int `json:"next_roll_min"`

	NodesPerHop  []int `json:"nodes_per_hop,omitempty"`
	ScaledPerHop []int `json:"scaled_per_hop,omitempty"`

	// TrafficManagement NodeInfo cache (from the "[TM] ... cache entries" line).
	TMCacheUsed   int   `json:"tm_cache_used"`
	TMCacheTarget int   `json:"tm_cache_target"`
	TMCacheAt     int64 `json:"tm_cache_at,omitempty"`
}

// SetHopScale records the HopScaling main stats line.
func (s *Store) SetHopScale(h *fwparser.HopScale, t time.Time) {
	if h == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hopScale == nil {
		s.hopScale = &HopScaleStatus{}
	}
	hs := s.hopScale
	hs.Enabled = true
	hs.UpdatedAt = t.Unix()
	hs.Hop = h.Hop
	hs.HistActive = h.HistActive
	hs.FillPct = h.FillPct
	hs.SampNum, hs.SampDen = h.SampNum, h.SampDen
	hs.FiltNum, hs.FiltDen = h.FiltNum, h.FiltDen
	hs.Entries = h.Entries
	hs.LastCounted = h.LastCounted
	hs.PoliteNum, hs.PoliteDen = h.PoliteNum, h.PoliteDen
	hs.NextRollMin = h.NextRollMin
}

// SetHopScalePerHop records one of the per-hop distribution arrays. kind is
// "nodes" or "last scaled".
func (s *Store) SetHopScalePerHop(kind string, vals []int, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hopScale == nil {
		s.hopScale = &HopScaleStatus{}
	}
	s.hopScale.Enabled = true
	s.hopScale.UpdatedAt = t.Unix()
	cp := append([]int(nil), vals...)
	if kind == "nodes" {
		s.hopScale.NodesPerHop = cp
	} else {
		s.hopScale.ScaledPerHop = cp
	}
}

// SetTMCache records the TrafficManagement NodeInfo cache state.
func (s *Store) SetTMCache(c *fwparser.TMCache, t time.Time) {
	if c == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hopScale == nil {
		s.hopScale = &HopScaleStatus{}
	}
	s.hopScale.Enabled = true
	s.hopScale.TMCacheUsed = c.Used
	s.hopScale.TMCacheTarget = c.Target
	s.hopScale.TMCacheAt = t.Unix()
}

// HopScaleStatus returns a copy of the current snapshot. Enabled is false when
// no log line has been parsed yet.
func (s *Store) HopScaleStatus() HopScaleStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hopScale == nil {
		return HopScaleStatus{}
	}
	out := *s.hopScale
	out.NodesPerHop = append([]int(nil), s.hopScale.NodesPerHop...)
	out.ScaledPerHop = append([]int(nil), s.hopScale.ScaledPerHop...)
	return out
}
