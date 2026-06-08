// Package experiment defines sweep parameters for Prequal replication
// experiments and helpers for writing CSV results.
package experiment

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"time"
)

// Step is one data point in an experiment sweep.
type Step struct {
	// Label is the x-axis value shown in plots.
	Label string
	// ProbePoolSize overrides the LB probe pool size for this step.
	ProbePoolSize int
	// ProbeRateMultiplier overrides the LB probe rate for this step.
	ProbeRateMultiplier float64
}

// Experiment is an ordered set of steps to sweep.
type Experiment struct {
	Name  string
	Steps []Step
}

// s2 = sqrt(2)
var s2 = math.Sqrt2

// ExperimentA replicates Figure 8 from the Prequal paper:
// sweep probe rate as a multiple of the query rate at ~1.5× CPU allocation.
// ProbePoolSize is held fixed at 8; ProbeRateMultiplier varies.
func ExperimentA(baseQPS float64) Experiment {
	multipliers := []float64{4, 2 * s2, 2, s2, 1, 1 / s2, 0.5}
	steps := make([]Step, len(multipliers))
	for i, m := range multipliers {
		steps[i] = Step{
			Label:               fmt.Sprintf("%.2f", m),
			ProbePoolSize:       8,
			ProbeRateMultiplier: m,
		}
	}
	return Experiment{Name: "A", Steps: steps}
}

// ExperimentB is the original sweep: vary probe pool size at ~75% load.
// ProbeRateMultiplier is held fixed at 3.0; ProbePoolSize varies.
func ExperimentB() Experiment {
	sizes := []int{1, 2, 4, 8, 16, 24, 32}
	steps := make([]Step, len(sizes))
	for i, sz := range sizes {
		steps[i] = Step{
			Label:               fmt.Sprintf("%d", sz),
			ProbePoolSize:       sz,
			ProbeRateMultiplier: 3.0,
		}
	}
	return Experiment{Name: "B", Steps: steps}
}

// Row is one measurement written to CSV.
type Row struct {
	Experiment          string
	Algorithm           string
	StepLabel           string
	ProbePoolSize       int
	ProbeRateMultiplier float64
	P99Ms               float64
	P999Ms              float64
	RIF_P50             float64
	RIF_P75             float64
	RIF_P99             float64
	Total               int64
	Errors              int64
}

// CSVWriter wraps a csv.Writer and writes rows in a consistent schema.
type CSVWriter struct {
	w *csv.Writer
}

// NewCSVWriter opens path for appending (creates if not exists) and
// writes a header row if the file is new.
func NewCSVWriter(path string) (*CSVWriter, error) {
	isNew := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		isNew = true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open csv %s: %w", path, err)
	}

	cw := &CSVWriter{w: csv.NewWriter(f)}
	if isNew {
		cw.w.Write([]string{
			"experiment", "algorithm", "step_label",
			"probe_pool_size", "probe_rate_multiplier",
			"p99_ms", "p999_ms",
			"rif_p50", "rif_p75", "rif_p99",
			"total_requests", "errors",
			"timestamp",
		})
	}
	return cw, nil
}

// Write appends one row to the CSV.
func (cw *CSVWriter) Write(r Row) error {
	err := cw.w.Write([]string{
		r.Experiment, r.Algorithm, r.StepLabel,
		fmt.Sprintf("%d", r.ProbePoolSize),
		fmt.Sprintf("%.4f", r.ProbeRateMultiplier),
		fmt.Sprintf("%.3f", r.P99Ms),
		fmt.Sprintf("%.3f", r.P999Ms),
		fmt.Sprintf("%.3f", r.RIF_P50),
		fmt.Sprintf("%.3f", r.RIF_P75),
		fmt.Sprintf("%.3f", r.RIF_P99),
		fmt.Sprintf("%d", r.Total),
		fmt.Sprintf("%d", r.Errors),
		time.Now().UTC().Format(time.RFC3339),
	})
	cw.w.Flush()
	return err
}
