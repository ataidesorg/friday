package routing

import (
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

func TestPricesExactMicros(t *testing.T) {
	tbl, err := Prices(map[string]config.PriceConfig{
		"gpt":  {InputUSDPerMTok: 5, OutputUSDPerMTok: 15, CachedUSDPerMTok: 2.5},
		"free": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Price{InputPerMTok: 5_000_000, OutputPerMTok: 15_000_000, CachedPerMTok: 2_500_000}
	if tbl["gpt"] != want {
		t.Fatalf("gpt price %+v, want %+v", tbl["gpt"], want)
	}
	// 800 fresh input at $5 + 200 cached at $2.50 + 100 output at $15,
	// all per million tokens: 4000 + 500 + 1500 micros.
	u := core.Usage{InputTokens: 1000, OutputTokens: 100, CachedInputTokens: 200}
	if got := tbl["gpt"].Cost(u); got != 6000 {
		t.Fatalf("cost %d, want 6000", got)
	}
	if got := tbl["free"].Cost(u); got != 0 {
		t.Fatalf("free cost %d, want 0", got)
	}
}

func TestPricesRejectInvalid(t *testing.T) {
	_, err := Prices(map[string]config.PriceConfig{"bad": {OutputUSDPerMTok: -1}})
	if err == nil || !strings.Contains(err.Error(), "models.pricing.bad.output_usd_per_mtok") {
		t.Fatalf("err = %v", err)
	}
}
