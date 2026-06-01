package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// MemoriesTotal counts memories by type, domain, and status.
	MemoriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sage_memories_total",
		Help: "Total number of memories processed",
	}, []string{"type", "domain", "status"})

	// TxTotal counts transactions by type and result.
	TxTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sage_tx_total",
		Help: "Total number of transactions processed",
	}, []string{"type", "result"})

	// TxRejectedTotal counts rejected transactions by reason.
	TxRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sage_tx_rejected_total",
		Help: "Total number of rejected transactions",
	}, []string{"reason"})

	// VotesTotal counts votes by decision type.
	VotesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sage_votes_total",
		Help: "Total number of votes cast",
	}, []string{"decision"})

	// CorroborationsTotal counts corroborations.
	CorroborationsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sage_corroborations_total",
		Help: "Total number of corroborations",
	})

	// ChallengesTotal counts challenges.
	ChallengesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sage_challenges_total",
		Help: "Total number of challenges",
	})

	// TxDuration tracks transaction processing duration.
	TxDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sage_tx_duration_seconds",
		Help:    "Transaction processing duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"type"})

	// QueryLatency tracks query latency.
	QueryLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sage_query_latency_seconds",
		Help:    "Memory query latency in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
	}, []string{"domain"})

	// FinalizeBlockDuration tracks FinalizeBlock processing time.
	FinalizeBlockDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sage_finalize_block_duration_seconds",
		Help:    "FinalizeBlock processing duration in seconds",
		Buckets: prometheus.DefBuckets,
	})

	// ActiveMemories tracks active memories by status.
	ActiveMemories = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sage_active_memories",
		Help: "Number of active memories by status",
	}, []string{"status"})

	// ValidatorCount tracks the number of active validators.
	ValidatorCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sage_validator_count",
		Help: "Number of active validators",
	})

	// EpochCurrent tracks the current epoch number.
	EpochCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sage_epoch_current",
		Help: "Current epoch number",
	})

	// PoEWeight tracks PoE weight per validator.
	PoEWeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sage_poe_weight",
		Help: "PoE weight per validator",
	}, []string{"validator_id"})

	// ForkBranchTotal counts how often each consensus-rule fork gate
	// took the pre-fork vs post-fork branch. Operators watch this to
	// confirm the cutover landed live on a chain: pre rolls up to zero,
	// post climbs from zero, on the same block. fork="v8" is the v8.0
	// access-control fork (HasAccessOrAncestor / processAccessGrant
	// auto-register / TxTypeDomainReassign).
	ForkBranchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sage_fork_branch_total",
		Help: "Per-fork count of pre- vs post-fork branches taken inside fork-gated consensus handlers",
	}, []string{"fork", "branch"})
)

// RecordTx records a transaction metric.
func RecordTx(txType string, duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	TxTotal.WithLabelValues(txType, result).Inc()
	TxDuration.WithLabelValues(txType).Observe(duration.Seconds())
}

// RecordQuery records a query metric.
func RecordQuery(domain string, duration time.Duration) {
	QueryLatency.WithLabelValues(domain).Observe(duration.Seconds())
}

// SetPoEWeights publishes the per-validator sage_poe_weight gauge from a freshly
// computed normalized weight set (called once per epoch boundary). Reset() drops
// every prior series first, then the current set is repopulated — so a validator
// removed via governance does not leave a frozen, misleading last-known weight.
// Gauge writes are process-local and order-independent: this touches no BadgerDB
// key and no pendingWrite, so it never affects the AppHash.
func SetPoEWeights(weights map[string]float64) {
	PoEWeight.Reset()
	for id, w := range weights {
		PoEWeight.WithLabelValues(id).Set(w)
	}
}
