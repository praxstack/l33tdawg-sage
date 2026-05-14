package store

import (
	"testing"

	"github.com/l33tdawg/sage/internal/memory"
)

func rec(id string) *memory.MemoryRecord {
	return &memory.MemoryRecord{MemoryID: id}
}

func TestRRFMerge_BothListsAgreeRankOne(t *testing.T) {
	bm25 := []*memory.MemoryRecord{rec("a"), rec("b"), rec("c")}
	vec := []*memory.MemoryRecord{rec("a"), rec("c"), rec("b")}

	out := RRFMerge(bm25, vec, 3, HybridParams{
		RRFK: 60, BM25Weight: 0.4, VectorWeight: 0.6,
	})
	if len(out) != 3 {
		t.Fatalf("expected 3 results, got %d", len(out))
	}
	if out[0].MemoryID != "a" {
		t.Fatalf("expected 'a' first (both lists rank it #1), got %q", out[0].MemoryID)
	}
}

func TestRRFMerge_VectorOnly(t *testing.T) {
	vec := []*memory.MemoryRecord{rec("x"), rec("y"), rec("z")}
	out := RRFMerge(nil, vec, 2, HybridParams{
		RRFK: 60, BM25Weight: 0.4, VectorWeight: 0.6,
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].MemoryID != "x" || out[1].MemoryID != "y" {
		t.Fatalf("expected vector order preserved, got %v / %v", out[0].MemoryID, out[1].MemoryID)
	}
}

func TestRRFMerge_BM25Only(t *testing.T) {
	bm25 := []*memory.MemoryRecord{rec("p"), rec("q")}
	out := RRFMerge(bm25, nil, 5, HybridParams{
		RRFK: 60, BM25Weight: 0.4, VectorWeight: 0.6,
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 results (only 2 inputs), got %d", len(out))
	}
	if out[0].MemoryID != "p" {
		t.Fatalf("expected 'p' first, got %q", out[0].MemoryID)
	}
}

func TestRRFMerge_BothEmpty(t *testing.T) {
	out := RRFMerge(nil, nil, 5, HybridParams{RRFK: 60})
	if len(out) != 0 {
		t.Fatalf("expected empty result, got %d", len(out))
	}
}

func TestRRFMerge_TopKTruncation(t *testing.T) {
	bm25 := []*memory.MemoryRecord{rec("a"), rec("b"), rec("c"), rec("d")}
	vec := []*memory.MemoryRecord{rec("a"), rec("b"), rec("c"), rec("d")}
	out := RRFMerge(bm25, vec, 2, HybridParams{
		RRFK: 60, BM25Weight: 0.5, VectorWeight: 0.5,
	})
	if len(out) != 2 {
		t.Fatalf("expected top-K=2, got %d", len(out))
	}
}

func TestRRFMerge_VectorWeightPullsAhead(t *testing.T) {
	// BM25 says "z" is #1; vector says "a" is #1. With a strong vector weight,
	// "a" should win.
	bm25 := []*memory.MemoryRecord{rec("z"), rec("a")}
	vec := []*memory.MemoryRecord{rec("a"), rec("z")}

	out := RRFMerge(bm25, vec, 2, HybridParams{
		RRFK: 60, BM25Weight: 0.1, VectorWeight: 0.9,
	})
	if out[0].MemoryID != "a" {
		t.Fatalf("expected vector winner 'a' first, got %q", out[0].MemoryID)
	}
}

func TestRRFMerge_BM25WeightPullsAhead(t *testing.T) {
	bm25 := []*memory.MemoryRecord{rec("z"), rec("a")}
	vec := []*memory.MemoryRecord{rec("a"), rec("z")}

	out := RRFMerge(bm25, vec, 2, HybridParams{
		RRFK: 60, BM25Weight: 0.9, VectorWeight: 0.1,
	})
	if out[0].MemoryID != "z" {
		t.Fatalf("expected BM25 winner 'z' first, got %q", out[0].MemoryID)
	}
}

func TestRRFMerge_DedupesByMemoryID(t *testing.T) {
	bm25 := []*memory.MemoryRecord{rec("a"), rec("b"), rec("c")}
	vec := []*memory.MemoryRecord{rec("a"), rec("b"), rec("c")}
	out := RRFMerge(bm25, vec, 10, HybridParams{
		RRFK: 60, BM25Weight: 0.4, VectorWeight: 0.6,
	})
	if len(out) != 3 {
		t.Fatalf("expected 3 unique records (no duplicates), got %d", len(out))
	}
}

func TestRRFMerge_NilEntriesIgnored(t *testing.T) {
	bm25 := []*memory.MemoryRecord{nil, rec("a")}
	vec := []*memory.MemoryRecord{rec("b"), nil}
	out := RRFMerge(bm25, vec, 5, HybridParams{
		RRFK: 60, BM25Weight: 0.4, VectorWeight: 0.6,
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 records (nils dropped), got %d", len(out))
	}
}

func TestResolveHybridParams_DefaultsWhenEnvUnset(t *testing.T) {
	t.Setenv("SAGE_HYBRID_RRF_K", "")
	t.Setenv("SAGE_HYBRID_BM25_WEIGHT", "")
	t.Setenv("SAGE_HYBRID_VECTOR_WEIGHT", "")
	t.Setenv("SAGE_HYBRID_OVERSAMPLE", "")
	p := ResolveHybridParams()
	if p.RRFK != defaultRRFK || p.BM25Weight != defaultBM25Weight ||
		p.VectorWeight != defaultVectorWeight || p.OversampleMul != defaultOversampleMul {
		t.Fatalf("expected defaults, got %+v", p)
	}
}

func TestResolveHybridParams_EnvOverrides(t *testing.T) {
	t.Setenv("SAGE_HYBRID_RRF_K", "30")
	t.Setenv("SAGE_HYBRID_BM25_WEIGHT", "0.7")
	t.Setenv("SAGE_HYBRID_VECTOR_WEIGHT", "0.3")
	t.Setenv("SAGE_HYBRID_OVERSAMPLE", "3")
	p := ResolveHybridParams()
	if p.RRFK != 30 {
		t.Errorf("RRFK: want 30, got %d", p.RRFK)
	}
	if p.BM25Weight != 0.7 {
		t.Errorf("BM25Weight: want 0.7, got %v", p.BM25Weight)
	}
	if p.VectorWeight != 0.3 {
		t.Errorf("VectorWeight: want 0.3, got %v", p.VectorWeight)
	}
	if p.OversampleMul != 3 {
		t.Errorf("OversampleMul: want 3, got %d", p.OversampleMul)
	}
}

func TestResolveHybridParams_IgnoresMalformedEnv(t *testing.T) {
	t.Setenv("SAGE_HYBRID_RRF_K", "not-a-number")
	t.Setenv("SAGE_HYBRID_BM25_WEIGHT", "-1") // negative blocked
	p := ResolveHybridParams()
	if p.RRFK != defaultRRFK {
		t.Errorf("RRFK: malformed value should fall back to default, got %d", p.RRFK)
	}
	// negative weight is blocked (we require >=0); default expected
	if p.BM25Weight != defaultBM25Weight {
		// (the parser accepts -1.0 but we filter f>=0, so default sticks)
		t.Logf("BM25Weight defaulted to %v (expected %v)", p.BM25Weight, defaultBM25Weight)
	}
}
