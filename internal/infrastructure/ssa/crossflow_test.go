package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findFact 按 (source, target, kind) 查找边。
func findFact(t *testing.T, facts []*domain.Fact, source, target, kind string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return f
		}
	}
	t.Fatalf("fact not found: %s -> %s [%s]", source, target, kind)
	return nil
}

// findFactByKindPrefix 按 kind 与 source ID 前缀查找边（SSA 临时名 tN 不稳定）。
func findFactByKindPrefix(facts []*domain.Fact, kind domain.FactKind, srcPrefix string) *domain.Fact {
	for _, f := range facts {
		if f.Kind == kind && strings.HasPrefix(string(f.SourceID), srcPrefix) {
			return f
		}
	}
	return nil
}
