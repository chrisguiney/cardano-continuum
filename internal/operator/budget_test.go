package operator

import (
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestAdmitBudget(t *testing.T) {
	b := v1alpha1.BudgetSpec{MaxPools: 2, CPUMillis: 1000, MemoryMiB: 2048}
	small := Demand{CPUMillis: 400, MemoryMiB: 512}
	tests := []struct {
		name  string
		queue []Claim
		self  string
		limit string // "" means admitted
	}{
		{"fits", []Claim{{"a", small}, {"b", small}}, "b", ""},
		{"pods", []Claim{{"a", small}, {"b", small}, {"c", small}}, "c", "pods"},
		{"cpu", []Claim{{"a", Demand{900, 1}}, {"b", small}}, "b", "cpu"},
		{"memory", []Claim{{"a", Demand{1, 2000}}, {"b", small}}, "b", "memory"},
		{"earlier refused does not count", []Claim{{"a", Demand{2000, 1}}, {"b", small}}, "b", ""},
		{"first is admitted when alone fits", []Claim{{"a", small}, {"b", Demand{2000, 1}}}, "a", ""},
		{"missing", []Claim{{"a", small}}, "z", "not"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Admit(b, tt.queue, tt.self)
			if tt.limit == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.limit) {
				t.Fatalf("err %v, want one naming %q", err, tt.limit)
			}
		})
	}
}
