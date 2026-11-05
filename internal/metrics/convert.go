package metrics

import (
	"fmt"
	"sort"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// toTimeSeries converts a slice of Prometheus MetricFamily values into remote
// write TimeSeries, appending extraLabels to every series. Handles Counter,
// Gauge, Histogram, and Summary metric types. Histograms are expanded per the
// Prometheus convention (_bucket, _sum, _count).
func toTimeSeries(families []*dto.MetricFamily, extraLabels map[string]string) []pbTimeSeries {
	var out []pbTimeSeries
	for _, mf := range families {
		if mf == nil {
			continue
		}
		name := mf.GetName()
		for _, m := range mf.GetMetric() {
			ts := time.Now().UnixMilli()
			if m.GetTimestampMs() != 0 {
				ts = m.GetTimestampMs()
			}
			baseLabels := buildLabels(name, m.GetLabel(), extraLabels)

			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				if c := m.GetCounter(); c != nil {
					out = append(out, pbTimeSeries{
						Labels:  baseLabels,
						Samples: []pbSample{{Value: c.GetValue(), Timestamp: ts}},
					})
				}

			case dto.MetricType_GAUGE:
				if g := m.GetGauge(); g != nil {
					out = append(out, pbTimeSeries{
						Labels:  baseLabels,
						Samples: []pbSample{{Value: g.GetValue(), Timestamp: ts}},
					})
				}

			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				if h == nil {
					continue
				}
				// _bucket series
				for _, b := range h.GetBucket() {
					le := fmt.Sprintf("%g", b.GetUpperBound())
					labels := buildLabelsWithExtra(baseLabels, "le", le)
					out = append(out, pbTimeSeries{
						Labels:  labels,
						Samples: []pbSample{{Value: float64(b.GetCumulativeCount()), Timestamp: ts}},
					})
				}
				// +Inf bucket
				infLabels := buildLabelsWithExtra(baseLabels, "le", "+Inf")
				out = append(out, pbTimeSeries{
					Labels:  infLabels,
					Samples: []pbSample{{Value: float64(h.GetSampleCount()), Timestamp: ts}},
				})
				// _sum
				sumLabels := relabelName(baseLabels, name+"_sum")
				out = append(out, pbTimeSeries{
					Labels:  sumLabels,
					Samples: []pbSample{{Value: h.GetSampleSum(), Timestamp: ts}},
				})
				// _count
				countLabels := relabelName(baseLabels, name+"_count")
				out = append(out, pbTimeSeries{
					Labels:  countLabels,
					Samples: []pbSample{{Value: float64(h.GetSampleCount()), Timestamp: ts}},
				})

			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				if s == nil {
					continue
				}
				// quantile series
				for _, q := range s.GetQuantile() {
					qLabel := fmt.Sprintf("%g", q.GetQuantile())
					labels := buildLabelsWithExtra(baseLabels, "quantile", qLabel)
					out = append(out, pbTimeSeries{
						Labels:  labels,
						Samples: []pbSample{{Value: q.GetValue(), Timestamp: ts}},
					})
				}
				// _sum
				sumLabels := relabelName(baseLabels, name+"_sum")
				out = append(out, pbTimeSeries{
					Labels:  sumLabels,
					Samples: []pbSample{{Value: s.GetSampleSum(), Timestamp: ts}},
				})
				// _count
				countLabels := relabelName(baseLabels, name+"_count")
				out = append(out, pbTimeSeries{
					Labels:  countLabels,
					Samples: []pbSample{{Value: float64(s.GetSampleCount()), Timestamp: ts}},
				})

			case dto.MetricType_UNTYPED:
				if u := m.GetUntyped(); u != nil {
					out = append(out, pbTimeSeries{
						Labels:  baseLabels,
						Samples: []pbSample{{Value: u.GetValue(), Timestamp: ts}},
					})
				}
			}
		}
	}
	return out
}

// buildLabels constructs the sorted label slice for a time series,
// merging metric labels and extra static labels with __name__.
func buildLabels(name string, metricLabels []*dto.LabelPair, extra map[string]string) []pbLabel {
	m := make(map[string]string, len(metricLabels)+len(extra)+1)
	m["__name__"] = name
	for k, v := range extra {
		m[k] = v
	}
	for _, lp := range metricLabels {
		m[lp.GetName()] = lp.GetValue()
	}
	return sortedLabels(m)
}

// buildLabelsWithExtra copies labels and appends an additional label, re-sorting.
func buildLabelsWithExtra(base []pbLabel, key, value string) []pbLabel {
	m := make(map[string]string, len(base)+1)
	for _, l := range base {
		m[l.Name] = l.Value
	}
	m[key] = value
	return sortedLabels(m)
}

// relabelName returns a copy of labels with __name__ replaced by newName.
func relabelName(base []pbLabel, newName string) []pbLabel {
	m := make(map[string]string, len(base))
	for _, l := range base {
		m[l.Name] = l.Value
	}
	m["__name__"] = newName
	return sortedLabels(m)
}

// sortedLabels converts a map to a sorted slice of pbLabel.
// Prometheus requires labels to be sorted by name.
func sortedLabels(m map[string]string) []pbLabel {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	labels := make([]pbLabel, 0, len(keys))
	for _, k := range keys {
		labels = append(labels, pbLabel{Name: k, Value: m[k]})
	}
	return labels
}
