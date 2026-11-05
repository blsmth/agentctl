//go:build !darwin && !linux

package cmd

// processCWD has no known implementation on this OS. Returning "" is
// deliberate: healthDecideOrphans treats an unknown cwd as "never an
// orphan" (see health.go), which is the correct fail-safe default here --
// we would rather under-report than guess.
func processCWD(pid string) string {
	return ""
}
