package management

import "testing"

func TestCodexPlanWeightMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		planType string
		want     float64
	}{
		{name: "free", planType: "free", want: codexFreePlanWeight},
		{name: "pro", planType: "pro", want: codexProPlanWeight},
		{name: "plus", planType: "plus", want: 1},
		{name: "business", planType: "business", want: 1},
		{name: "team", planType: "team", want: 1},
		{name: "enterprise", planType: "enterprise", want: 1},
		{name: "unknown", planType: "guest", want: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := codexPlanWeight(tt.planType, 0, 0); got != tt.want {
				t.Fatalf("codexPlanWeight(%q) = %v, want %v", tt.planType, got, tt.want)
			}
			if got := inferCodexTotalUsageMultiplier(tt.planType, 0, 0); got != tt.want {
				t.Fatalf("inferCodexTotalUsageMultiplier(%q) = %v, want %v", tt.planType, got, tt.want)
			}
		})
	}
}

func TestCodexPlanWeightOverridesStillApplyToFreeAndPro(t *testing.T) {
	t.Parallel()

	if got := codexPlanWeight("free", 0.5, 9); got != 0.5 {
		t.Fatalf("free override = %v, want 0.5", got)
	}
	if got := codexPlanWeight("pro", 0.5, 9); got != 9 {
		t.Fatalf("pro override = %v, want 9", got)
	}
	if got := codexPlanWeight("team", 0.5, 9); got != 1 {
		t.Fatalf("team should stay in default bucket, got %v", got)
	}
}
