package tui

import (
	"errors"
	"reflect"
	"testing"
)

func TestInferAgentFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []ghLabel
		want   string
	}{
		{
			name:   "exact agent name match",
			labels: []ghLabel{{Name: "go-engineer"}},
			want:   "go-engineer",
		},
		{
			name:   "exact python agent name match",
			labels: []ghLabel{{Name: "python-engineer"}},
			want:   "python-engineer",
		},
		{
			name:   "alias go -> go-engineer",
			labels: []ghLabel{{Name: "go"}},
			want:   "go-engineer",
		},
		{
			name:   "alias python -> python-engineer",
			labels: []ghLabel{{Name: "python"}},
			want:   "python-engineer",
		},
		{
			name:   "alias terraform -> terraform-engineer",
			labels: []ghLabel{{Name: "terraform"}},
			want:   "terraform-engineer",
		},
		{
			name:   "infra is not mapped (private agent)",
			labels: []ghLabel{{Name: "infra"}},
			want:   "",
		},
		{
			name:   "no matching label returns empty",
			labels: []ghLabel{{Name: "bug"}, {Name: "enhancement"}},
			want:   "",
		},
		{
			name:   "ready label ignored",
			labels: []ghLabel{{Name: "ready"}},
			want:   "",
		},
		{
			name:   "multiple labels - exact match wins over alias",
			labels: []ghLabel{{Name: "go-engineer"}, {Name: "python"}},
			want:   "go-engineer",
		},
		{
			name:   "multiple labels - alias picked up after exact fails",
			labels: []ghLabel{{Name: "bug"}, {Name: "python"}},
			want:   "python-engineer",
		},
		{
			name:   "empty labels",
			labels: []ghLabel{},
			want:   "",
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inferAgentFromLabels(tc.labels)
			if got != tc.want {
				t.Errorf("inferAgentFromLabels(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

func TestHasLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []ghLabel
		target string
		want   bool
	}{
		{
			name:   "label present",
			labels: []ghLabel{{Name: "ready"}, {Name: "in-progress"}},
			target: "in-progress",
			want:   true,
		},
		{
			name:   "label absent",
			labels: []ghLabel{{Name: "ready"}},
			target: "in-progress",
			want:   false,
		},
		{
			name:   "empty labels",
			labels: []ghLabel{},
			target: "in-progress",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasLabel(tc.labels, tc.target)
			if got != tc.want {
				t.Errorf("hasLabel(%v, %q) = %v, want %v", tc.labels, tc.target, got, tc.want)
			}
		})
	}
}

// TestAutoPollSkipsInProgress verifies that issues with "in-progress" label are
// not dispatched by the autopoll logic. We test the filtering by checking that
// hasLabel correctly identifies in-progress issues which autoPollCmd would skip.
func TestAutoPollSkipsInProgress(t *testing.T) {
	inProgressIssues := []struct {
		labels []ghLabel
		skip   bool
	}{
		{
			labels: []ghLabel{{Name: "ready"}, {Name: "in-progress"}},
			skip:   true,
		},
		{
			labels: []ghLabel{{Name: "ready"}},
			skip:   false,
		},
		{
			labels: []ghLabel{{Name: "ready"}, {Name: "go-engineer"}},
			skip:   false,
		},
		{
			labels: []ghLabel{{Name: "in-progress"}},
			skip:   true,
		},
	}

	for _, tc := range inProgressIssues {
		got := hasLabel(tc.labels, "in-progress")
		if got != tc.skip {
			t.Errorf("hasLabel(%v, \"in-progress\") = %v, want %v", tc.labels, got, tc.skip)
		}
	}
}

func TestRunAutoPoll(t *testing.T) {
	const issuesJSON = `[
		{"number":1,"title":"a","labels":[{"name":"python"}]},
		{"number":2,"title":"b","labels":[{"name":"in-progress"}]},
		{"number":3,"title":"c","labels":[{"name":"infra"}]}
	]`
	tests := []struct {
		name         string
		dispatchErr  func(issue int) error
		wantDisp     []int
		wantFailures int
		wantAgents   map[int]string
	}{
		{
			name:        "success records dispatched",
			dispatchErr: func(int) error { return nil },
			wantDisp:    []int{1, 3},
			wantAgents:  map[int]string{1: "python-engineer", 3: "go-engineer"},
		},
		{
			name:         "exec failure is not recorded",
			dispatchErr:  func(int) error { return errors.New("boom") },
			wantFailures: 2,
		},
		{
			name: "partial failure",
			dispatchErr: func(i int) error {
				if i == 1 {
					return errors.New("boom")
				}
				return nil
			},
			wantDisp:     []int{3},
			wantFailures: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agents := map[int]string{}
			res := runAutoPoll("o/r", autoPollRunner{
				list: func(string) ([]byte, error) { return []byte(issuesJSON), nil },
				dispatch: func(i int, a string) error {
					agents[i] = a
					return tc.dispatchErr(i)
				},
			})
			var got []int
			for _, d := range res.dispatched {
				got = append(got, d.issueNumber)
				if want, ok := tc.wantAgents[d.issueNumber]; ok && d.agentName != want {
					t.Errorf("#%d agent = %q, want %q", d.issueNumber, d.agentName, want)
				}
			}
			if !reflect.DeepEqual(got, tc.wantDisp) {
				t.Errorf("dispatched = %v, want %v", got, tc.wantDisp)
			}
			if len(res.failures) != tc.wantFailures {
				t.Errorf("failures = %v, want %d", res.failures, tc.wantFailures)
			}
			if _, ok := agents[2]; ok {
				t.Error("in-progress issue #2 must not be dispatched")
			}
		})
	}
}

func TestRunAutoPollListError(t *testing.T) {
	res := runAutoPoll("o/r", autoPollRunner{
		list: func(string) ([]byte, error) { return nil, errors.New("gh down") },
	})
	if res.err == nil {
		t.Fatal("want error")
	}
}
