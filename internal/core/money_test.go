package core

import (
	"errors"
	"math"
	"testing"
)

func TestUSDFromFloat(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want USDMicros
		ok   bool
	}{
		{"one and a half", 1.5, 1_500_000, true},
		{"zero", 0, 0, true},
		{"rounds", 0.0000005, 1, true},
		{"negative", -1, 0, false},
		{"nan", math.NaN(), 0, false},
		{"inf", math.Inf(1), 0, false},
		{"overflow", 1e15, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := USDFromFloat(tc.in)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUSDMicrosString(t *testing.T) {
	cases := map[USDMicros]string{1_234_567: "$1.234567", 0: "$0.000000", 5: "$0.000005", -2_500_000: "-$2.500000"}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int64(in), got, want)
		}
	}
	if got := USDMicros(2_500_000).Float(); got != 2.5 {
		t.Errorf("Float() = %v, want 2.5", got)
	}
}

func TestUSDMicrosAdd(t *testing.T) {
	sum, err := USDMicros(1).Add(2)
	if err != nil || sum != 3 {
		t.Fatalf("1+2 = %d, %v", sum, err)
	}
	if _, err := USDMicros(math.MaxInt64).Add(1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("overflow err = %v, want ErrInvalidInput", err)
	}
	if _, err := USDMicros(math.MinInt64).Add(-1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("underflow err = %v, want ErrInvalidInput", err)
	}
}
