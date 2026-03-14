package handlers

import (
	"testing"

	"golang.org/x/net/context"
)

func TestReportObservedServiceTier_UsesDefaultFallbackWhenContextMissing(t *testing.T) {
	called := false
	gotAuthID := ""
	gotTier := ""
	SetDefaultObservedServiceTierCallback(func(authID string, serviceTier string) {
		called = true
		gotAuthID = authID
		gotTier = serviceTier
	})
	defer SetDefaultObservedServiceTierCallback(nil)

	ReportObservedServiceTier(context.Background(), "auth-1", "priority")

	if !called {
		t.Fatal("default observed service tier callback was not called")
	}
	if gotAuthID != "auth-1" {
		t.Fatalf("authID = %q, want %q", gotAuthID, "auth-1")
	}
	if gotTier != "priority" {
		t.Fatalf("serviceTier = %q, want %q", gotTier, "priority")
	}
}
