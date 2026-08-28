package core

import (
	"fmt"
	"math"
)

// USDMicros is an exact amount of US dollars in millionths. Integer money
// keeps cost accounting free of floating-point drift.
type USDMicros int64

// MicrosPerUSD is the number of micros in one dollar.
const MicrosPerUSD USDMicros = 1_000_000

// maxUSD is the largest dollar amount representable without overflow.
const maxUSD = float64(math.MaxInt64) / float64(MicrosPerUSD)

// USDFromFloat converts a non-negative finite dollar amount to micros,
// rounding to the nearest micro.
func USDFromFloat(usd float64) (USDMicros, error) {
	switch {
	case math.IsNaN(usd) || math.IsInf(usd, 0):
		return 0, fmt.Errorf("%w: usd amount must be finite", ErrInvalidInput)
	case usd < 0:
		return 0, fmt.Errorf("%w: usd amount must not be negative", ErrInvalidInput)
	case usd > maxUSD:
		return 0, fmt.Errorf("%w: usd amount overflows micros", ErrInvalidInput)
	}
	return USDMicros(math.Round(usd * float64(MicrosPerUSD))), nil
}

// Float returns the amount in dollars as a float64 (for display only).
func (m USDMicros) Float() float64 { return float64(m) / float64(MicrosPerUSD) }

// String formats the amount as "$1.234567".
func (m USDMicros) String() string {
	sign := ""
	if m < 0 {
		sign = "-"
		m = -m
	}
	return fmt.Sprintf("%s$%d.%06d", sign, m/MicrosPerUSD, m%MicrosPerUSD)
}

// Add returns m+o, failing instead of wrapping on overflow.
func (m USDMicros) Add(o USDMicros) (USDMicros, error) {
	sum := m + o
	if (o > 0 && sum < m) || (o < 0 && sum > m) {
		return 0, fmt.Errorf("%w: usd micros overflow", ErrInvalidInput)
	}
	return sum, nil
}
