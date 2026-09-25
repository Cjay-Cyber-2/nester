package service

import (
	"context"
	"testing"
)

// TestGetYieldComparison_AggregatesMultiPoolProtocolsByTVLWeight pins the
// core contract (nester#950): a protocol with more than one active pool comes
// back as one entry, with APY and risk TVL-weighted across its pools and TVL
// summed. blend has two pools here; aqua has one, so aqua's entry is just its
// own pool's numbers unweighted.
func TestGetYieldComparison_AggregatesMultiPoolProtocolsByTVLWeight(t *testing.T) {
	payload := `{"status":"success","data":[
		{"pool":"b1","project":"blend","symbol":"USDC","apy":6.0,"tvlUsd":2000000,"chain":"Stellar"},
		{"pool":"b2","project":"blend","symbol":"XLM","apy":8.23,"tvlUsd":2200000,"chain":"Stellar"},
		{"pool":"a1","project":"aqua","symbol":"AQUA","apy":11.0,"tvlUsd":890000,"chain":"Stellar"}
	]}`
	ts := newMockDeFiLlamaServer(t, 200, payload, nil)
	defer ts.Close()

	svc := NewYieldService(ts.URL)
	got, err := svc.GetYieldComparison(context.Background(), "Stellar", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Protocols) != 2 {
		t.Fatalf("got %d protocols, want 2: %+v", len(got.Protocols), got.Protocols)
	}

	var blend, aqua *YieldComparisonEntry
	for i := range got.Protocols {
		switch got.Protocols[i].Protocol {
		case "blend":
			blend = &got.Protocols[i]
		case "aqua":
			aqua = &got.Protocols[i]
		}
	}
	if blend == nil || aqua == nil {
		t.Fatalf("expected both blend and aqua present, got %+v", got.Protocols)
	}

	wantBlendTVL := 2_000_000.0 + 2_200_000.0
	if blend.TVLUSD != wantBlendTVL {
		t.Errorf("blend tvl_usd = %v, want %v", blend.TVLUSD, wantBlendTVL)
	}
	wantBlendAPY := (6.0*2_000_000.0 + 8.23*2_200_000.0) / wantBlendTVL
	if diff := blend.CurrentAPY - wantBlendAPY; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("blend current_apy = %v, want %v", blend.CurrentAPY, wantBlendAPY)
	}

	if aqua.TVLUSD != 890_000 || aqua.CurrentAPY != 11.0 {
		t.Errorf("aqua entry wrong: %+v", aqua)
	}
}

// TestGetYieldComparison_SortsDescendingByCurrentAPY pins the documented
// ordering: highest APY first, so a comparison view can render top-to-bottom
// without its own sort.
func TestGetYieldComparison_SortsDescendingByCurrentAPY(t *testing.T) {
	payload := `{"status":"success","data":[
		{"pool":"p1","project":"low-apy","symbol":"A","apy":2.0,"tvlUsd":500000,"chain":"Stellar"},
		{"pool":"p2","project":"high-apy","symbol":"B","apy":15.0,"tvlUsd":500000,"chain":"Stellar"},
		{"pool":"p3","project":"mid-apy","symbol":"C","apy":8.0,"tvlUsd":500000,"chain":"Stellar"}
	]}`
	ts := newMockDeFiLlamaServer(t, 200, payload, nil)
	defer ts.Close()

	svc := NewYieldService(ts.URL)
	got, err := svc.GetYieldComparison(context.Background(), "Stellar", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Protocols) != 3 {
		t.Fatalf("got %d protocols, want 3: %+v", len(got.Protocols), got.Protocols)
	}
	wantOrder := []string{"high-apy", "mid-apy", "low-apy"}
	for i, want := range wantOrder {
		if got.Protocols[i].Protocol != want {
			t.Errorf("position %d = %q, want %q (full order: %+v)", i, got.Protocols[i].Protocol, want, got.Protocols)
		}
	}
}

// TestGetYieldComparison_LimitTruncatesAfterSorting proves the limit is
// applied to the sorted, aggregated result — the highest-APY protocols
// survive truncation, not an arbitrary prefix of the unsorted pool list.
func TestGetYieldComparison_LimitTruncatesAfterSorting(t *testing.T) {
	payload := `{"status":"success","data":[
		{"pool":"p1","project":"low-apy","symbol":"A","apy":2.0,"tvlUsd":500000,"chain":"Stellar"},
		{"pool":"p2","project":"high-apy","symbol":"B","apy":15.0,"tvlUsd":500000,"chain":"Stellar"},
		{"pool":"p3","project":"mid-apy","symbol":"C","apy":8.0,"tvlUsd":500000,"chain":"Stellar"}
	]}`
	ts := newMockDeFiLlamaServer(t, 200, payload, nil)
	defer ts.Close()

	svc := NewYieldService(ts.URL)
	got, err := svc.GetYieldComparison(context.Background(), "Stellar", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Protocols) != 1 {
		t.Fatalf("got %d protocols, want 1", len(got.Protocols))
	}
	if got.Protocols[0].Protocol != "high-apy" {
		t.Errorf("kept protocol = %q, want the highest-APY one (high-apy)", got.Protocols[0].Protocol)
	}
}

// TestGetYieldComparison_LimitClampsToValidRange proves the service defends
// its own bounds rather than trusting the caller: <=0 defaults to 100, and
// anything above 100 is clamped down to it. The handler also validates this
// range before calling in, but the service must not silently misbehave if
// called directly or from a future caller that skips that check.
func TestGetYieldComparison_LimitClampsToValidRange(t *testing.T) {
	payload := `{"status":"success","data":[{"pool":"p1","project":"solo","symbol":"A","apy":5.0,"tvlUsd":500000,"chain":"Stellar"}]}`

	for _, limit := range []int{0, -5, 500} {
		ts := newMockDeFiLlamaServer(t, 200, payload, nil)
		svc := NewYieldService(ts.URL)
		got, err := svc.GetYieldComparison(context.Background(), "Stellar", limit)
		ts.Close()
		if err != nil {
			t.Fatalf("limit=%d: unexpected error: %v", limit, err)
		}
		if len(got.Protocols) != 1 {
			t.Fatalf("limit=%d: got %d protocols, want the single solo entry to survive clamping", limit, len(got.Protocols))
		}
	}
}

// TestGetYieldComparison_UpstreamErrorPropagates proves a DeFiLlama failure
// surfaces as an error rather than an empty, misleadingly-successful
// comparison.
func TestGetYieldComparison_UpstreamErrorPropagates(t *testing.T) {
	ts := newMockDeFiLlamaServer(t, 500, "", nil)
	defer ts.Close()

	svc := NewYieldService(ts.URL)
	_, err := svc.GetYieldComparison(context.Background(), "Stellar", 100)
	if err == nil {
		t.Fatal("expected an error when the upstream fails, got nil")
	}
}

// TestGetYieldComparison_RiskTierIsDerivedFromScore proves each entry carries
// a risk_tier consistent with RiskTierForScore(risk_score), not a raw or
// stale value.
func TestGetYieldComparison_RiskTierIsDerivedFromScore(t *testing.T) {
	// tvlUsd below $1M and apyPct7d volatility push risk_score into the high
	// tier under the service's own scoring (mirrors mixedTierDefiLlama's
	// p_high fixture in yield_risk_tier_test.go).
	t.Setenv("YIELD_MIN_TVL_USD", "1000")
	payload := `{"status":"success","data":[
		{"pool":"p_high","project":"risky","symbol":"FOO","apy":10.0,"apyBase":1.0,"apyReward":9.0,"tvlUsd":50000,"apyPct7d":25.0,"chain":"Stellar"}
	]}`
	ts := newMockDeFiLlamaServer(t, 200, payload, nil)
	defer ts.Close()

	svc := NewYieldService(ts.URL)
	got, err := svc.GetYieldComparison(context.Background(), "Stellar", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Protocols) != 1 {
		t.Fatalf("got %d protocols, want 1: %+v", len(got.Protocols), got.Protocols)
	}
	entry := got.Protocols[0]
	if want := RiskTierForScore(entry.RiskScore); entry.RiskTier != want {
		t.Errorf("risk_tier = %q, want %q (derived from risk_score %v)", entry.RiskTier, want, entry.RiskScore)
	}
	if entry.RiskTier != RiskTierHigh {
		t.Errorf("risk_tier = %q, want high for this fixture", entry.RiskTier)
	}
}
