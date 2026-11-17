package tui

import (
	"slices"
	"testing"
)

func TestHerdLaunchArgs(t *testing.T) {
	want := []string{"launch", "--background", "--permission-mode", "auto"}
	if got := herdLaunchArgs(); !slices.Equal(got, want) {
		t.Errorf("herdLaunchArgs() = %v, want %v", got, want)
	}
}
