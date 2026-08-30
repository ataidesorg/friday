package routing

import (
	"fmt"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

// Prices converts the models.pricing config table into a PriceTable. Each
// USD-per-million-tokens float becomes exact micros here, once, so every
// later cost computation stays in integer arithmetic. A model absent from
// the table has unknown cost; a cost cap then fails closed at selection.
func Prices(cfg map[string]config.PriceConfig) (PriceTable, error) {
	t := make(PriceTable, len(cfg))
	for model, p := range cfg {
		in, err := usdRate(model, "input_usd_per_mtok", p.InputUSDPerMTok)
		if err != nil {
			return nil, err
		}
		out, err := usdRate(model, "output_usd_per_mtok", p.OutputUSDPerMTok)
		if err != nil {
			return nil, err
		}
		cached, err := usdRate(model, "cached_usd_per_mtok", p.CachedUSDPerMTok)
		if err != nil {
			return nil, err
		}
		t[model] = Price{InputPerMTok: in, OutputPerMTok: out, CachedPerMTok: cached}
	}
	return t, nil
}

func usdRate(model, field string, v float64) (core.USDMicros, error) {
	m, err := core.USDFromFloat(v)
	if err != nil {
		return 0, fmt.Errorf("models.pricing.%s.%s: %w", model, field, err)
	}
	return m, nil
}
