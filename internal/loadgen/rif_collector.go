package loadgen

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// RIFStats holds quantiles scraped from the rif_at_dispatch histogram.
type RIFStats struct {
	P50, P75, P99 float64
}

// ScrapeRIF fetches metricsURL (the LB's /metrics endpoint), parses the
// rif_at_dispatch histogram for the given algorithm label, and returns
// approximate quantiles computed from the cumulative bucket counts.
func ScrapeRIF(metricsURL, algorithm string) (RIFStats, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(metricsURL)
	if err != nil {
		return RIFStats{}, fmt.Errorf("scrape %s: %w", metricsURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RIFStats{}, fmt.Errorf("read metrics body: %w", err)
	}

	parser := expfmt.NewDecoder(strings.NewReader(string(body)), expfmt.NewFormat(expfmt.TypeTextPlain))

	for {
		var mf dto.MetricFamily
		if err := parser.Decode(&mf); err != nil {
			break
		}
		if mf.GetName() != "rif_at_dispatch" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if !hasLabel(m, "algorithm", algorithm) {
				continue
			}
			return histogramQuantiles(m.GetHistogram()), nil
		}
	}
	return RIFStats{}, fmt.Errorf("rif_at_dispatch{algorithm=%q} not found in %s", algorithm, metricsURL)
}

func hasLabel(m *dto.Metric, key, value string) bool {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == key && lp.GetValue() == value {
			return true
		}
	}
	return false
}

// histogramQuantiles computes p50/p75/p99 from a Prometheus histogram by
// linear interpolation between bucket boundaries.
func histogramQuantiles(h *dto.Histogram) RIFStats {
	if h == nil || h.GetSampleCount() == 0 {
		return RIFStats{}
	}

	type bucket struct {
		upper float64
		count uint64
	}

	buckets := make([]bucket, 0, len(h.GetBucket()))
	for _, b := range h.GetBucket() {
		buckets = append(buckets, bucket{
			upper: b.GetUpperBound(),
			count: b.GetCumulativeCount(),
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].upper < buckets[j].upper })

	total := h.GetSampleCount()

	interpolate := func(p float64) float64 {
		target := uint64(float64(total) * p)
		if target == 0 {
			target = 1
		}
		var prev bucket
		for _, b := range buckets {
			if b.count >= target {
				if prev.count == 0 {
					return b.upper
				}
				width := b.upper - prev.upper
				frac := float64(target-prev.count) / float64(b.count-prev.count)
				return prev.upper + frac*width
			}
			prev = b
		}
		return buckets[len(buckets)-1].upper
	}

	return RIFStats{
		P50: interpolate(0.50),
		P75: interpolate(0.75),
		P99: interpolate(0.99),
	}
}
