package appvalidator

import (
	"fmt"
	"regexp"
	"strings"
)

// VoteResult is what each validator returns.
type VoteResult struct {
	ValidatorName string
	Decision      string // "accept", "reject", "abstain"
	Reason        string
}

// Validator validates a proposed memory.
type Validator interface {
	Name() string
	Validate(content string, contentHash string, domain string, memType string, confidence float64) VoteResult
}

// --- SentinelValidator ---

// SentinelValidator always accepts. Ensures at least 1 vote is always positive.
type SentinelValidator struct{}

func (v *SentinelValidator) Name() string { return "sentinel" }

func (v *SentinelValidator) Validate(content, contentHash, domain, memType string, confidence float64) VoteResult {
	return VoteResult{
		ValidatorName: v.Name(),
		Decision:      "accept",
		Reason:        "baseline accept",
	}
}

// --- DedupValidator ---

// ContentHashChecker returns true if a content hash already exists in committed memories.
type ContentHashChecker func(hash string) (bool, error)

// DedupValidator rejects duplicate content based on content hash.
type DedupValidator struct {
	findByContentHash ContentHashChecker
}

func NewDedupValidator(checker ContentHashChecker) *DedupValidator {
	return &DedupValidator{findByContentHash: checker}
}

func (v *DedupValidator) Name() string { return "dedup" }

func (v *DedupValidator) Validate(content, contentHash, domain, memType string, confidence float64) VoteResult {
	exists, err := v.findByContentHash(contentHash)
	if err != nil {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "abstain",
			Reason:        fmt.Sprintf("hash check error: %v", err),
		}
	}
	if exists {
		hashPrefix := contentHash
		if len(hashPrefix) > 8 {
			hashPrefix = hashPrefix[:8]
		}
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        fmt.Sprintf("duplicate content already exists (hash: %s)", hashPrefix),
		}
	}
	return VoteResult{
		ValidatorName: v.Name(),
		Decision:      "accept",
		Reason:        "no duplicate found",
	}
}

// --- QualityValidator ---

var (
	greetingPatterns = []string{
		"user said hi",
		"user greeted",
		"session started",
		"brain online",
		"brain is online",
		"no action taken",
	}
	reflectionHeaderRe = regexp.MustCompile(`(?i)^\[Task Reflection\]`)
	emptyHeaderRe      = regexp.MustCompile(`(?i)^#\s+\S+(\s+\S+)*\s*$`)
)

// QualityValidator rejects low-quality or noisy content.
type QualityValidator struct{}

func (v *QualityValidator) Name() string { return "quality" }

func (v *QualityValidator) Validate(content, contentHash, domain, memType string, confidence float64) VoteResult {
	// Greeting noise (check before length so we get the right rejection reason)
	lower := strings.ToLower(strings.TrimSpace(content))
	for _, pattern := range greetingPatterns {
		if lower == pattern {
			return VoteResult{
				ValidatorName: v.Name(),
				Decision:      "reject",
				Reason:        "low-value observation: matches noise pattern",
			}
		}
	}

	// Too short
	if len(content) < 20 {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        fmt.Sprintf("content too short (%d chars, minimum 20)", len(content)),
		}
	}

	// Empty reflection header
	if reflectionHeaderRe.MatchString(content) && len(content) < 60 {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        "empty reflection header without substance",
		}
	}

	// Empty header like "# SAGE Project Memory"
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "#") && emptyHeaderRe.MatchString(trimmed) {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        "empty header",
		}
	}

	return VoteResult{
		ValidatorName: v.Name(),
		Decision:      "accept",
		Reason:        "content passes quality checks",
	}
}

// --- ConsistencyValidator ---

// ConsistencyValidator checks confidence thresholds and required fields.
type ConsistencyValidator struct{}

func (v *ConsistencyValidator) Name() string { return "consistency" }

func (v *ConsistencyValidator) Validate(content, contentHash, domain, memType string, confidence float64) VoteResult {
	if confidence < 0.3 {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        fmt.Sprintf("confidence too low (%.2f)", confidence),
		}
	}

	if memType == "fact" && confidence < 0.7 {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        fmt.Sprintf("facts require confidence >= 0.7 (got %.2f)", confidence),
		}
	}

	if domain == "" {
		return VoteResult{
			ValidatorName: v.Name(),
			Decision:      "reject",
			Reason:        "domain required",
		}
	}

	return VoteResult{
		ValidatorName: v.Name(),
		Decision:      "accept",
		Reason:        "passes consistency checks",
	}
}
