package cronedit

import (
	"slices"
	"testing"
)

func TestParseSimpleRoundTrip(t *testing.T) {
	tests := []struct {
		spec string
		want SimpleSchedule
	}{
		{"5 * * * *", SimpleSchedule{Kind: KindHourly, Minute: 5}},
		{"0 9 * * *", SimpleSchedule{Kind: KindDaily, Hour: 9}},
		{"30 7 * * 1-5", SimpleSchedule{Kind: KindWeekdays, Minute: 30, Hour: 7}},
		{"15 8 * * 1", SimpleSchedule{Kind: KindDays, Minute: 15, Hour: 8, Days: []int{1}}},
		{"0 10 * * 0,6", SimpleSchedule{Kind: KindDays, Hour: 10, Days: []int{0, 6}}},
		{"0 6 * * 1,3,5", SimpleSchedule{Kind: KindDays, Hour: 6, Days: []int{1, 3, 5}}},
		{"59 23 * * 0,1,2,3,4,5,6", SimpleSchedule{Kind: KindDays, Minute: 59, Hour: 23, Days: []int{0, 1, 2, 3, 4, 5, 6}}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got, ok := ParseSimple(tt.spec)
			if !ok || got.Kind != tt.want.Kind || got.Minute != tt.want.Minute || got.Hour != tt.want.Hour || !slices.Equal(got.Days, tt.want.Days) {
				t.Fatalf("ParseSimple = %+v, %v; want %+v", got, ok, tt.want)
			}
			spec, err := got.Spec()
			if err != nil || spec != tt.spec {
				t.Errorf("Spec() = %q, %v; want the original %q back", spec, err, tt.spec)
			}
		})
	}
}

// TestParseSimpleRejectsNonRepresentable: anything Spec would not emit
// byte for byte goes to Custom mode, so an untouched expression is never
// rewritten.
func TestParseSimpleRejectsNonRepresentable(t *testing.T) {
	for _, spec := range []string{
		"", "@daily", "@hourly", "*/15 * * * *", "0 9-17 * * *", "0 9 1 * *", "0 9 * 1 *",
		"30 7 * * MON-FRI", "30 7 * * mon", "30 7 * * 1-3", "30 07 * * *", "05 * * * *",
		"0 9 * * 5,1", "0 9 * * 1,1", "0 9 * * 7", "5 * * * 1", "0 0,12 * * *", "60 * * * *",
		"0 24 * * *", "0 9 * * *  extra", "garbage",
	} {
		if s, ok := ParseSimple(spec); ok {
			t.Errorf("ParseSimple(%q) = %+v, want ok=false", spec, s)
		}
	}
	// Extra whitespace between fields is the same expression.
	if _, ok := ParseSimple(" 30  7 * * 1-5 "); !ok {
		t.Error("whitespace variant rejected")
	}
}

func TestSimpleSpec(t *testing.T) {
	tests := []struct {
		in      SimpleSchedule
		want    string
		wantErr bool
	}{
		{in: SimpleSchedule{Kind: KindHourly, Minute: 7, Hour: 99}, want: "7 * * * *"},
		{in: SimpleSchedule{Kind: KindDays, Hour: 8, Days: []int{5, 1, 5}}, want: "0 8 * * 1,5"},
		{in: SimpleSchedule{Kind: KindDays, Hour: 8}, wantErr: true},
		{in: SimpleSchedule{Kind: KindDays, Hour: 8, Days: []int{7}}, wantErr: true},
		{in: SimpleSchedule{Kind: KindDaily, Hour: 24}, wantErr: true},
		{in: SimpleSchedule{Kind: KindDaily, Minute: -1}, wantErr: true},
		{in: SimpleSchedule{Kind: "monthly"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := tt.in.Spec()
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%+v.Spec() = %q, %v; want %q (err %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
