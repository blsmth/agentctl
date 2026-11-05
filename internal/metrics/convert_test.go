package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

func counterFamily(name string, value float64, labelPairs ...*dto.LabelPair) *dto.MetricFamily {
	mt := dto.MetricType_COUNTER
	return &dto.MetricFamily{
		Name: strPtr(name),
		Type: &mt,
		Metric: []*dto.Metric{
			{
				Label:   labelPairs,
				Counter: &dto.Counter{Value: f64Ptr(value)},
			},
		},
	}
}

func gaugeFamily(name string, value float64) *dto.MetricFamily {
	mt := dto.MetricType_GAUGE
	return &dto.MetricFamily{
		Name: strPtr(name),
		Type: &mt,
		Metric: []*dto.Metric{
			{Gauge: &dto.Gauge{Value: f64Ptr(value)}},
		},
	}
}

func histogramFamily(name string, sum float64, count uint64, buckets []*dto.Bucket) *dto.MetricFamily {
	mt := dto.MetricType_HISTOGRAM
	return &dto.MetricFamily{
		Name: strPtr(name),
		Type: &mt,
		Metric: []*dto.Metric{
			{
				Histogram: &dto.Histogram{
					SampleCount: &count,
					SampleSum:   &sum,
					Bucket:      buckets,
				},
			},
		},
	}
}

func TestToTimeSeries_Counter(t *testing.T) {
	fam := counterFamily("my_counter", 42.0)
	series := toTimeSeries([]*dto.MetricFamily{fam}, nil)

	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}

	ts := series[0]

	// Find __name__ label.
	nameLabel := findLabel(ts.Labels, "__name__")
	if nameLabel != "my_counter" {
		t.Errorf("__name__ = %q, want %q", nameLabel, "my_counter")
	}

	if len(ts.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(ts.Samples))
	}
	if ts.Samples[0].Value != 42.0 {
		t.Errorf("sample value = %v, want 42.0", ts.Samples[0].Value)
	}
}

func TestToTimeSeries_Gauge(t *testing.T) {
	fam := gaugeFamily("my_gauge", 3.14)
	series := toTimeSeries([]*dto.MetricFamily{fam}, nil)

	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}
	if series[0].Samples[0].Value != 3.14 {
		t.Errorf("sample value = %v, want 3.14", series[0].Samples[0].Value)
	}
}

func TestToTimeSeries_Histogram(t *testing.T) {
	ub1 := 0.1
	ub2 := 1.0
	cc1 := uint64(5)
	cc2 := uint64(9)
	buckets := []*dto.Bucket{
		{UpperBound: &ub1, CumulativeCount: &cc1},
		{UpperBound: &ub2, CumulativeCount: &cc2},
	}
	fam := histogramFamily("my_hist", 12.5, 10, buckets)
	series := toTimeSeries([]*dto.MetricFamily{fam}, nil)

	// Expect: 2 buckets + +Inf + _sum + _count = 5
	if len(series) != 5 {
		t.Fatalf("expected 5 series for histogram, got %d", len(series))
	}

	// Find +Inf bucket series (name=my_hist AND le=+Inf).
	var infSeries *pbTimeSeries
	for i := range series {
		if findLabel(series[i].Labels, "__name__") == "my_hist" &&
			findLabel(series[i].Labels, "le") == "+Inf" {
			infSeries = &series[i]
			break
		}
	}
	if infSeries == nil {
		t.Fatal("expected a series named my_hist with le=+Inf")
	}
	if infSeries.Samples[0].Value != 10 {
		t.Errorf("expected +Inf count=10, got %v", infSeries.Samples[0].Value)
	}

	// Find _sum and _count.
	sumS := findByLabel(series, "__name__", "my_hist_sum")
	if sumS == nil {
		t.Fatal("missing my_hist_sum series")
	}
	if sumS.Samples[0].Value != 12.5 {
		t.Errorf("_sum = %v, want 12.5", sumS.Samples[0].Value)
	}

	countS := findByLabel(series, "__name__", "my_hist_count")
	if countS == nil {
		t.Fatal("missing my_hist_count series")
	}
	if countS.Samples[0].Value != 10 {
		t.Errorf("_count = %v, want 10", countS.Samples[0].Value)
	}
}

func TestToTimeSeries_StaticLabels(t *testing.T) {
	fam := counterFamily("c", 1.0)
	extra := map[string]string{"instance": "host1", "env": "prod"}
	series := toTimeSeries([]*dto.MetricFamily{fam}, extra)

	if len(series) == 0 {
		t.Fatal("expected at least 1 series")
	}
	for _, ts := range series {
		if v := findLabel(ts.Labels, "instance"); v != "host1" {
			t.Errorf("instance label = %q, want %q", v, "host1")
		}
		if v := findLabel(ts.Labels, "env"); v != "prod" {
			t.Errorf("env label = %q, want %q", v, "prod")
		}
	}
}

func TestToTimeSeries_MetricLabels(t *testing.T) {
	lp := &dto.LabelPair{
		Name:  proto.String("agent"),
		Value: proto.String("go-engineer"),
	}
	fam := counterFamily("my_counter", 7.0, lp)
	series := toTimeSeries([]*dto.MetricFamily{fam}, nil)

	if len(series) == 0 {
		t.Fatal("expected at least 1 series")
	}
	if v := findLabel(series[0].Labels, "agent"); v != "go-engineer" {
		t.Errorf("agent label = %q, want %q", v, "go-engineer")
	}
}

func TestToTimeSeries_NilFamily(t *testing.T) {
	series := toTimeSeries([]*dto.MetricFamily{nil}, nil)
	if len(series) != 0 {
		t.Errorf("expected 0 series for nil family, got %d", len(series))
	}
}

func TestToTimeSeries_LabelsSorted(t *testing.T) {
	fam := gaugeFamily("g", 1.0)
	extra := map[string]string{"zzz": "last", "aaa": "first"}
	series := toTimeSeries([]*dto.MetricFamily{fam}, extra)
	if len(series) == 0 {
		t.Fatal("expected at least 1 series")
	}
	labels := series[0].Labels
	for i := 1; i < len(labels); i++ {
		if labels[i].Name < labels[i-1].Name {
			t.Errorf("labels not sorted: %v >= %v", labels[i-1].Name, labels[i].Name)
		}
	}
}

// findLabel returns the value of the label with the given name, or "".
func findLabel(labels []pbLabel, name string) string {
	for _, l := range labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// findByLabel returns the first pbTimeSeries where labelName==labelValue.
func findByLabel(series []pbTimeSeries, labelName, labelValue string) *pbTimeSeries {
	for i := range series {
		if findLabel(series[i].Labels, labelName) == labelValue {
			return &series[i]
		}
	}
	return nil
}
