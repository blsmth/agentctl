package tui

import (
	"slices"
	"testing"
)

func TestFleetLaunchArgs(t *testing.T) {
	want := []string{"launch", "--background", "--permission-mode", "auto"}
	if got := fleetLaunchArgs(); !slices.Equal(got, want) {
		t.Errorf("fleetLaunchArgs() = %v, want %v", got, want)
	}
}
