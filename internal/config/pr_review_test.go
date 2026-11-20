package config

import "testing"

func TestEffectivePRReview(t *testing.T) {
	tests := []struct {
		in        string
		want      string
		wantValid bool
	}{
		{"", PRReviewAlways, true},
		{"always", PRReviewAlways, true},
		{"optional", PRReviewOptional, true},
		{"off", PRReviewOff, true},
		{"OFF", PRReviewAlways, false},
		{"never", PRReviewAlways, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			c := Config{PRReview: tt.in}
			if got := c.EffectivePRReview(); got != tt.want {
				t.Errorf("EffectivePRReview(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := ValidPRReview(tt.in); got != tt.wantValid {
				t.Errorf("ValidPRReview(%q) = %v, want %v", tt.in, got, tt.wantValid)
			}
			if (c.PRReviewWarning() == "") != tt.wantValid {
				t.Errorf("PRReviewWarning(%q) = %q, validity %v", tt.in, c.PRReviewWarning(), tt.wantValid)
			}
		})
	}
}

func TestMergeConfigPRReviewOverride(t *testing.T) {
	base := &Config{PRReview: PRReviewAlways}
	mergeConfig(base, &Config{PRReview: PRReviewOff})
	if base.PRReview != PRReviewOff {
		t.Errorf("project override lost: %q", base.PRReview)
	}
	mergeConfig(base, &Config{})
	if base.PRReview != PRReviewOff {
		t.Errorf("empty overlay changed value: %q", base.PRReview)
	}
}
