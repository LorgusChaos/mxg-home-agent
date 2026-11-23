// Package soc estimates LiFePO4 state of charge from voltage. Rough: the curve is flat and load/charge shift the
// voltage, so this is only a stopgap until JK BMS gives the real SOC.
package soc

import "sort"

// per-cell resting voltage -> percent, ascending
var curve = []struct{ v, pct float64 }{
	{2.50, 0}, {3.00, 10}, {3.20, 20}, {3.22, 30}, {3.25, 40}, {3.26, 50},
	{3.27, 60}, {3.30, 70}, {3.32, 80}, {3.35, 90}, {3.40, 100},
}

// FromVoltage returns estimated percent (0..100) for a pack of n cells in series.
func FromVoltage(packV float64, cells int) float64 {
	if cells <= 0 {
		cells = 8
	}
	v := packV / float64(cells)
	if v <= curve[0].v {
		return 0
	}
	for i := 1; i < len(curve); i++ {
		if v <= curve[i].v {
			a, b := curve[i-1], curve[i]
			return a.pct + (v-a.v)/(b.v-a.v)*(b.pct-a.pct)
		}
	}
	return 100
}

// Median of a window; used to smooth voltage before estimating.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
