package store

import (
	"testing"
	"time"

	"mesh-reader/internal/fwparser"
)

func TestHopScaleStatus(t *testing.T) {
	s := New(10)
	if hs := s.HopScaleStatus(); hs.Enabled {
		t.Fatal("initial HopScaleStatus should be disabled")
	}

	now := time.Now()
	s.SetHopScale(&fwparser.HopScale{
		Hop: 7, FillPct: 70, SampNum: 1, SampDen: 8,
		FiltNum: 1, FiltDen: 8, Entries: 90, PoliteNum: 2, PoliteDen: 4,
		NextRollMin: 60,
	}, now)
	s.SetHopScalePerHop("nodes", []int{0, 1, 2, 0, 0, 0, 0, 0}, now)
	s.SetTMCache(&fwparser.TMCache{Used: 1, Target: 1945}, now)

	hs := s.HopScaleStatus()
	if !hs.Enabled {
		t.Fatal("HopScaleStatus should be enabled after parsing")
	}
	if hs.Hop != 7 || hs.FillPct != 70 || hs.Entries != 90 {
		t.Errorf("unexpected hop state: %+v", hs)
	}
	if hs.NextRollMin != 60 {
		t.Errorf("NextRollMin = %d, want 60", hs.NextRollMin)
	}
	if len(hs.NodesPerHop) != 8 || hs.NodesPerHop[2] != 2 {
		t.Errorf("NodesPerHop = %v", hs.NodesPerHop)
	}
	if hs.TMCacheUsed != 1 || hs.TMCacheTarget != 1945 {
		t.Errorf("TM cache = %d/%d, want 1/1945", hs.TMCacheUsed, hs.TMCacheTarget)
	}
	if hs.UpdatedAt != now.Unix() || hs.TMCacheAt != now.Unix() {
		t.Errorf("timestamps not set: UpdatedAt=%d TMCacheAt=%d", hs.UpdatedAt, hs.TMCacheAt)
	}

	// Returned slice must be a copy (mutating it must not affect the store).
	hs.NodesPerHop[0] = 99
	if s.HopScaleStatus().NodesPerHop[0] == 99 {
		t.Error("HopScaleStatus returned a shared slice, expected a copy")
	}
}
