package measures

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// sortedParts is one series' days as the stored figures' fold holds them: each day's samples
// sorted.
func sortedParts(series string, days [][]float64) []DayPart {
	parts := make([]DayPart, len(days))
	for index, samples := range days {
		sorted := slices.Clone(samples)
		slices.Sort(sorted)
		parts[index] = DayPart{Series: series, Day: time.Date(2026, 1, 1+index, 0, 0, 0, 0, time.UTC), Samples: sorted}
	}
	return parts
}

// mergedOf is every sample of days merged and sorted, as a read of the rows folds them.
func mergedOf(days [][]float64) []float64 {
	var merged []float64
	for _, samples := range days {
		merged = append(merged, samples...)
	}
	slices.Sort(merged)
	return merged
}

// TestASelectedRankIsTheSortsRankBitForBit: over day lists of every shape a series' days take (one
// sample, none, many, repeated values, values that differ in their last bit, negatives, zeros and
// very large and very small magnitudes), every rank selected across the days' sorted lists, the
// largest and each quantile a fold reads are the merge and sort's, bit for bit, as the stored
// figures' fold needs them to be (SortedSamples).
func TestASelectedRankIsTheSortsRankBitForBit(t *testing.T) {
	random := rand.New(rand.NewPCG(7, 11))
	value := func() float64 {
		switch random.IntN(6) {
		case 0:
			return float64(random.IntN(5))
		case 1:
			return -random.Float64() * 100
		case 2:
			return math.Nextafter(1, 2)
		case 3:
			return random.Float64() * 1e300
		case 4:
			return random.Float64() * 1e-300
		}
		return random.Float64() * 1000
	}
	for trial := range 200 {
		days := make([][]float64, 1+random.IntN(85))
		for index := range days {
			samples := make([]float64, random.IntN(30))
			for k := range samples {
				samples[k] = value()
			}
			days[index] = samples
		}
		merged := mergedOf(days)
		selected := MergedSamples(sortedParts("minutes", days), "minutes")
		if selected.Len() != len(merged) {
			t.Fatalf("trial %d: the selection holds %d samples, the merge %d", trial, selected.Len(), len(merged))
		}
		if len(merged) == 0 {
			continue
		}
		// Every rank of a merge of up to 100 samples; of a longer one, its two ends and 100 ranks
		// at random.
		ranks := []int{0, len(merged) - 1}
		for k := range min(len(merged), 100) {
			if len(merged) > 100 {
				k = random.IntN(len(merged))
			}
			ranks = append(ranks, k)
		}
		for _, k := range ranks {
			if got, want := selected.At(k), merged[k]; math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("trial %d: rank %d selected is %v, the sort's is %v", trial, k, got, want)
			}
		}
		if got, want := selected.Max(), merged[len(merged)-1]; math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("trial %d: the largest selected is %v, the sort's is %v", trial, got, want)
		}
		for _, p := range []float64{0, 0.1, 0.5, 0.9, 1} {
			if got, want := selected.Quantile(p), Quantile(merged, p); math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("trial %d: quantile %v selected is %v, the sort's is %v", trial, p, got, want)
			}
		}
	}
}

// TestAPartNotSortedMergesEveryPart: a fold of parts one of which holds its samples in the order
// they were stored, as a part read for a request does, merges and sorts every part's samples,
// whatever the others hold; so does one whose sorted list holds a -0 or a NaN, which is no list a
// sort holds bit for bit (SortableSamples), since a -0 beside a 0 may sort to either side of it.
func TestAPartNotSortedMergesEveryPart(t *testing.T) {
	days := [][]float64{{3, 1}, {2, 5}, {4}}
	parts := sortedParts("minutes", days)
	if selected := MergedSamples(parts, "minutes"); selected.lists == nil {
		t.Fatal("a fold over parts whose samples are all sorted merges them")
	}
	parts[1] = DayPart{Series: "minutes", Samples: []float64{5, 2}}
	merged := MergedSamples(parts, "minutes")
	if merged.lists != nil {
		t.Fatalf("a fold over a part whose samples are not sorted selects across the lists %v", merged.lists)
	}
	for k, want := range []float64{1, 2, 3, 4, 5} {
		if got := merged.At(k); got != want {
			t.Errorf("rank %d is %v, want %v", k, got, want)
		}
	}
	negativeZero := math.Copysign(0, -1)
	parts[1] = DayPart{Series: "minutes", Samples: []float64{negativeZero, 0}}
	if signed := MergedSamples(parts, "minutes"); signed.lists != nil {
		t.Fatalf("a fold over a part whose sorted list holds a -0 selects across the lists %v", signed.lists)
	}
	if !SortableSamples([]float64{0, 1, -2}) {
		t.Error("a list of a 0, a 1 and a -2 reads as one a sort does not hold bit for bit")
	}
	for _, samples := range [][]float64{{1, negativeZero}, {math.NaN()}} {
		if SortableSamples(samples) {
			t.Errorf("the list %v reads as one a sort holds bit for bit", samples)
		}
	}
}
