package store

import (
	"os"
	"sort"
	"strconv"

	"github.com/l33tdawg/sage/internal/memory"
)

// Reciprocal Rank Fusion default constants. RRFK shapes how aggressively
// lower-ranked items contribute; bm25/vector weights bias toward one index.
// Tunable at runtime via env so operators can experiment without a rebuild.
const (
	defaultRRFK          = 60
	defaultBM25Weight    = 0.4
	defaultVectorWeight  = 0.6
	defaultOversampleMul = 2
)

// HybridParams controls the fusion. Zero-valued fields fall back to env or
// compile-time defaults so callers can stay terse.
type HybridParams struct {
	RRFK          int     // smoothing constant; larger = flatter contribution curve
	BM25Weight    float64 // weight applied to BM25 rank contribution
	VectorWeight  float64 // weight applied to vector rank contribution
	OversampleMul int     // each index requests TopK*OversampleMul before merging
}

// ResolveHybridParams applies env overrides on top of compile-time defaults.
// Env names: SAGE_HYBRID_RRF_K, SAGE_HYBRID_BM25_WEIGHT, SAGE_HYBRID_VECTOR_WEIGHT,
// SAGE_HYBRID_OVERSAMPLE.
func ResolveHybridParams() HybridParams {
	p := HybridParams{
		RRFK:          defaultRRFK,
		BM25Weight:    defaultBM25Weight,
		VectorWeight:  defaultVectorWeight,
		OversampleMul: defaultOversampleMul,
	}
	if v := os.Getenv("SAGE_HYBRID_RRF_K"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.RRFK = n
		}
	}
	if v := os.Getenv("SAGE_HYBRID_BM25_WEIGHT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			p.BM25Weight = f
		}
	}
	if v := os.Getenv("SAGE_HYBRID_VECTOR_WEIGHT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			p.VectorWeight = f
		}
	}
	if v := os.Getenv("SAGE_HYBRID_OVERSAMPLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			p.OversampleMul = n
		}
	}
	return p
}

// rrfScore is the weighted Reciprocal Rank Fusion contribution for one rank.
// rank is 1-based. A missing index passes math.MaxInt as the rank; the caller
// must guard against that — we just multiply weight / (k+rank).
func rrfScore(weight float64, rank, k int) float64 {
	if rank <= 0 {
		return 0
	}
	return weight / float64(k+rank)
}

// RRFMerge fuses BM25 and vector result lists by memory_id using weighted
// Reciprocal Rank Fusion. Both inputs are pre-ranked (best first). The merged
// slice is sorted by combined score descending and truncated to topK.
//
// A record present in only one list still scores; its missing rank contributes
// zero. The first occurrence of a record wins for fields like Content — both
// lists return the same record, so this is just a tie-breaker.
func RRFMerge(
	bm25 []*memory.MemoryRecord,
	vector []*memory.MemoryRecord,
	topK int,
	params HybridParams,
) []*memory.MemoryRecord {
	if topK <= 0 {
		topK = 10
	}
	if params.RRFK <= 0 {
		params.RRFK = defaultRRFK
	}

	type scored struct {
		record *memory.MemoryRecord
		score  float64
	}

	merged := make(map[string]*scored, len(bm25)+len(vector))

	for i, r := range bm25 {
		if r == nil {
			continue
		}
		s := rrfScore(params.BM25Weight, i+1, params.RRFK)
		merged[r.MemoryID] = &scored{record: r, score: s}
	}

	for i, r := range vector {
		if r == nil {
			continue
		}
		s := rrfScore(params.VectorWeight, i+1, params.RRFK)
		if existing, ok := merged[r.MemoryID]; ok {
			existing.score += s
		} else {
			merged[r.MemoryID] = &scored{record: r, score: s}
		}
	}

	ranked := make([]*scored, 0, len(merged))
	for _, s := range merged {
		ranked = append(ranked, s)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if topK > len(ranked) {
		topK = len(ranked)
	}
	results := make([]*memory.MemoryRecord, topK)
	for i := 0; i < topK; i++ {
		results[i] = ranked[i].record
	}
	return results
}
