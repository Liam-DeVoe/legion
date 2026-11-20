package measures

import (
	"math"
	"slices"
	"sort"
)

// SortedSamples is every sample of one series among a set of parts, in ascending order: the one
// list a quantile reads (MergedSamples). It is that list merged and sorted, or, where each part's
// samples already ascend and hold no -0 and no NaN (SortableSamples), the parts' lists as they
// stand, of which each rank a fold reads is selected across the lists when it is read (At).
//
// The stored figures fold one series' days into many ranges, each of up to 85 days (evals,
// figures.go), so the fold sorts each day's samples once, and every range that holds the day
// reads them sorted: a range's median and 90th percentile are four ranks selected across its
// days' lists, where a merge and a sort of every sample is paid again for each range. k8
// measured both on the bench's busiest quantile series (dispatch://INFRA-2916, k8 phase C,
// qbench): for the ranges a change 60 days back rewrites, selection took 19-26 ms where the merge
// and sort took 108-227 ms, and the two answered every range alike, bit for bit. A rank selected
// is the value the sort puts at that rank: the lists hold no -0 and no NaN, and of the values
// left no two that compare equal differ in their bits.
type SortedSamples struct {
	// merged is the sorted merge, nil where lists answers.
	merged []float64
	// lists is the parts' sorted lists, each non-empty, read by selection; nil where merged
	// answers.
	lists [][]float64
	n     int
}

// Len is how many samples s holds.
func (s SortedSamples) Len() int { return s.n }

// At is the k-th smallest sample (from 0) of s; k is below Len.
func (s SortedSamples) At(k int) float64 {
	if s.lists == nil {
		return s.merged[k]
	}
	if k == s.n-1 {
		largest := math.Inf(-1)
		for _, list := range s.lists {
			largest = max(largest, list[len(list)-1])
		}
		return largest
	}
	return kth(s.lists, k)
}

// Max is the largest sample of s, which holds at least one.
func (s SortedSamples) Max() float64 { return s.At(s.n - 1) }

// Quantile is p's quantile of s, which holds at least one sample, by Quantile's rule.
func (s SortedSamples) Quantile(p float64) float64 { return quantileAt(s.n, s.At, p) }

// MergedSamples is every sample of series among parts in ascending order: the one list a quantile
// reads. Where each part of series among parts holds samples that already ascend and hold no -0
// and no NaN (as the stored figures' fold leaves them), it reads their lists as they stand
// (SortedSamples), which answers every rank as the sort would; else it merges them, in the parts'
// order, and sorts the merge. A part read for a request holds its samples in the order they were
// stored, whose first descent ends the check.
func MergedSamples(parts []DayPart, series string) SortedSamples {
	var lists [][]float64
	n, ascending := 0, true
	for _, part := range parts {
		if part.Series != series || len(part.Samples) == 0 {
			continue
		}
		lists = append(lists, part.Samples)
		n += len(part.Samples)
		ascending = ascending && ascends(part.Samples)
	}
	switch {
	case len(lists) == 0:
		return SortedSamples{}
	case ascending && len(lists) == 1:
		return SortedSamples{merged: lists[0], n: n}
	case ascending:
		return SortedSamples{lists: lists, n: n}
	}
	merged := make([]float64, 0, n)
	for _, list := range lists {
		merged = append(merged, list...)
	}
	slices.Sort(merged)
	return SortedSamples{merged: merged, n: n}
}

// ascends reports whether samples are in ascending order and hold no -0 and no NaN
// (SortableSamples).
func ascends(samples []float64) bool {
	for index, value := range samples {
		if math.IsNaN(value) || value == 0 && math.Signbit(value) || index > 0 && value < samples[index-1] {
			return false
		}
	}
	return true
}

// SortableSamples reports whether samples hold no -0 and no NaN, the lists MergedSamples reads as
// they stand once sorted: sorted, such a list holds each value at the one rank selection finds it
// at. A -0 beside a 0, which the sort holds equal, may stand on either side of it after a sort,
// and a quantile that reads the two would differ in its sign of zero between the sort and a
// selection.
func SortableSamples(samples []float64) bool {
	for _, value := range samples {
		if math.IsNaN(value) || value == 0 && math.Signbit(value) {
			return false
		}
	}
	return true
}

// quantileAt is p's quantile of the n ascending values at(0) to at(n-1), n at least 1, linear
// between the two nearest ranks, each product converted to float64 before the sum it feeds, for
// the reason Quantile gives.
func quantileAt(n int, at func(int) float64, p float64) float64 {
	i := float64(float64(n-1) * p)
	lo := int(math.Floor(i))
	if lo+1 >= n {
		return at(n - 1)
	}
	low := at(lo)
	return low + float64((at(lo+1)-low)*(i-float64(lo)))
}

// kth is the k-th smallest value (from 0) of the union of lists, each sorted ascending and
// non-empty, exactly: each round counts every list's values below and equal to a pivot, the
// weighted median of the lists' middle values (so a round leaves at least a quarter of what
// remains behind), by binary search, and keeps the side that holds rank k.
func kth(lists [][]float64, k int) float64 {
	lo := make([]int, len(lists))
	hi := make([]int, len(lists))
	below := make([]int, len(lists))
	through := make([]int, len(lists))
	for i, list := range lists {
		hi[i] = len(list)
	}
	type candidate struct {
		value  float64
		weight int
	}
	candidates := make([]candidate, 0, len(lists))
	for {
		candidates = candidates[:0]
		total := 0
		for i, list := range lists {
			if n := hi[i] - lo[i]; n > 0 {
				candidates = append(candidates, candidate{list[lo[i]+n/2], n})
				total += n
			}
		}
		slices.SortFunc(candidates, func(a, b candidate) int {
			switch {
			case a.value < b.value:
				return -1
			case a.value > b.value:
				return 1
			}
			return 0
		})
		pivot, seen := candidates[len(candidates)-1].value, 0
		for _, c := range candidates {
			seen += c.weight
			if 2*seen >= total {
				pivot = c.value
				break
			}
		}
		less, equal := 0, 0
		for i, list := range lists {
			window := list[lo[i]:hi[i]]
			below[i] = lo[i] + sort.SearchFloat64s(window, pivot)
			through[i] = lo[i] + sort.Search(len(window), func(j int) bool { return window[j] > pivot })
			less += below[i] - lo[i]
			equal += through[i] - below[i]
		}
		switch {
		case k < less:
			copy(hi, below)
		case k < less+equal:
			return pivot
		default:
			k -= less + equal
			copy(lo, through)
		}
	}
}
