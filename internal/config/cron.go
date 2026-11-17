package config

import "time"

// Cron defaults (see docs/design/cron.md).
const (
	DefaultCronMinInterval      = 15 * time.Minute
	DefaultCronMaxConcurrent    = 2
	DefaultCronCatchupWindow    = 6 * time.Hour
	DefaultCronUsageFiveHourPct = 80
	DefaultCronUsageSevenDayPct = 90
)

// CronUsageThresholds are the usage-gate percentages: a scheduled run is
// skipped when the pushed five_hour or seven_day used percentage is at or
// above its threshold. A zero field means "use the default" (global) or "use
// the global value" (per job).
type CronUsageThresholds struct {
	FiveHour float64 `json:"five_hour,omitempty"`
	SevenDay float64 `json:"seven_day,omitempty"`
}

// CronConfig is the `cron` block: global defaults for `agentctl cron`. Every
// key is global-only (see sanitizeProjectConfig): a project-local config can
// neither switch scheduling on nor loosen its throttles. The jobs themselves
// live in <GlobalDir>/schedules.json, never in config.json.
type CronConfig struct {
	// Enabled is the kill switch. nil (unset) means enabled; an explicit
	// false makes `agentctl cron tick` a no-op.
	Enabled *bool `json:"enabled,omitempty"`
	// MinInterval is a Go duration: `cron add` rejects a schedule that fires
	// more often. Default 15m.
	MinInterval string `json:"min_interval,omitempty"`
	// MaxConcurrent caps live, not-done scheduled windows across all jobs.
	// Default 2.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// SkipIfUsageAbove is the default usage gate. Default five_hour 80,
	// seven_day 90.
	SkipIfUsageAbove CronUsageThresholds `json:"skip_if_usage_above,omitzero"`
	// CatchupWindow is a Go duration: the oldest missed fire `catchup:
	// latest` still runs. Default 6h.
	CatchupWindow string `json:"catchup_window,omitempty"`
}

// EnabledOrDefault reports the kill switch (default true).
func (c CronConfig) EnabledOrDefault() bool { return c.Enabled == nil || *c.Enabled }

// EffectiveMinInterval returns MinInterval, or the default when unset or
// invalid.
func (c CronConfig) EffectiveMinInterval() time.Duration {
	return durationOr(c.MinInterval, DefaultCronMinInterval)
}

// EffectiveCatchupWindow returns CatchupWindow, or the default when unset or
// invalid.
func (c CronConfig) EffectiveCatchupWindow() time.Duration {
	return durationOr(c.CatchupWindow, DefaultCronCatchupWindow)
}

// EffectiveMaxConcurrent returns MaxConcurrent, or the default when unset or
// not positive.
func (c CronConfig) EffectiveMaxConcurrent() int {
	if c.MaxConcurrent > 0 {
		return c.MaxConcurrent
	}
	return DefaultCronMaxConcurrent
}

// EffectiveUsageThresholds returns the usage gate with defaults applied per
// field.
func (c CronConfig) EffectiveUsageThresholds() CronUsageThresholds {
	t := c.SkipIfUsageAbove
	if t.FiveHour <= 0 {
		t.FiveHour = DefaultCronUsageFiveHourPct
	}
	if t.SevenDay <= 0 {
		t.SevenDay = DefaultCronUsageSevenDayPct
	}
	return t
}

func durationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
