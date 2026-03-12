package appvalidator

import (
	"errors"
	"testing"
)

func TestSentinelAlwaysAccepts(t *testing.T) {
	v := &SentinelValidator{}
	result := v.Validate("anything", "hash123", "general", "observation", 0.5)
	if result.Decision != "accept" {
		t.Errorf("expected accept, got %s", result.Decision)
	}
	if result.Reason != "baseline accept" {
		t.Errorf("expected 'baseline accept', got %s", result.Reason)
	}
	if result.ValidatorName != "sentinel" {
		t.Errorf("expected validator name 'sentinel', got %s", result.ValidatorName)
	}
}

func TestDedupDetectsExisting(t *testing.T) {
	checker := func(hash string) (bool, error) { return true, nil }
	v := NewDedupValidator(checker)

	result := v.Validate("some content", "abcdef1234567890", "general", "observation", 0.8)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "duplicate content already exists (hash: abcdef12)" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestDedupAcceptsNew(t *testing.T) {
	checker := func(hash string) (bool, error) { return false, nil }
	v := NewDedupValidator(checker)

	result := v.Validate("some content", "abcdef1234567890", "general", "observation", 0.8)
	if result.Decision != "accept" {
		t.Errorf("expected accept, got %s", result.Decision)
	}
}

func TestDedupAbstainsOnError(t *testing.T) {
	checker := func(hash string) (bool, error) { return false, errors.New("db error") }
	v := NewDedupValidator(checker)

	result := v.Validate("some content", "hash123", "general", "observation", 0.8)
	if result.Decision != "abstain" {
		t.Errorf("expected abstain on error, got %s", result.Decision)
	}
}

func TestQualityRejectsTooShort(t *testing.T) {
	v := &QualityValidator{}
	result := v.Validate("short", "hash", "general", "observation", 0.8)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "content too short (5 chars, minimum 20)" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestQualityRejectsGreetings(t *testing.T) {
	v := &QualityValidator{}
	greetings := []string{
		"user said hi",
		"User Said Hi",
		"USER GREETED",
		"session started",
		"brain online",
		"Brain Is Online",
		"No Action Taken",
	}
	for _, g := range greetings {
		result := v.Validate(g, "hash", "general", "observation", 0.8)
		if result.Decision != "reject" {
			t.Errorf("expected reject for %q, got %s", g, result.Decision)
		}
		if result.Reason != "low-value observation: matches noise pattern" {
			t.Errorf("unexpected reason for %q: %s", g, result.Reason)
		}
	}
}

func TestQualityRejectsEmptyReflection(t *testing.T) {
	v := &QualityValidator{}
	result := v.Validate("[Task Reflection] Boot", "hash", "general", "observation", 0.8)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "empty reflection header without substance" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestQualityAcceptsSubstantive(t *testing.T) {
	v := &QualityValidator{}
	content := "The ABCI pipeline processes memory transactions through CometBFT consensus before committing to state."
	result := v.Validate(content, "hash", "general", "observation", 0.8)
	if result.Decision != "accept" {
		t.Errorf("expected accept, got %s", result.Decision)
	}
}

func TestQualityRejectsEmptyHeader(t *testing.T) {
	v := &QualityValidator{}
	result := v.Validate("# SAGE Project Memory", "hash", "general", "observation", 0.8)
	if result.Decision != "reject" {
		t.Errorf("expected reject for empty header, got %s", result.Decision)
	}
	if result.Reason != "empty header" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestConsistencyRejectsLowConfidence(t *testing.T) {
	v := &ConsistencyValidator{}
	result := v.Validate("valid content here for testing", "hash", "general", "observation", 0.2)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "confidence too low (0.20)" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestConsistencyRejectsLowConfFact(t *testing.T) {
	v := &ConsistencyValidator{}
	result := v.Validate("valid content here for testing", "hash", "general", "fact", 0.5)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "facts require confidence >= 0.7 (got 0.50)" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestConsistencyRejectsEmptyDomain(t *testing.T) {
	v := &ConsistencyValidator{}
	result := v.Validate("valid content here for testing", "hash", "", "observation", 0.8)
	if result.Decision != "reject" {
		t.Errorf("expected reject, got %s", result.Decision)
	}
	if result.Reason != "domain required" {
		t.Errorf("unexpected reason: %s", result.Reason)
	}
}

func TestConsistencyAcceptsValid(t *testing.T) {
	v := &ConsistencyValidator{}
	result := v.Validate("valid content here for testing", "hash", "general", "observation", 0.8)
	if result.Decision != "accept" {
		t.Errorf("expected accept, got %s", result.Decision)
	}
}
