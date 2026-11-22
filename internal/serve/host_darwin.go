//go:build darwin

package serve

import "golang.org/x/sys/unix"

// memoryPressure reads the kernel's pressure level (1 normal, 2 warn,
// 4 critical) without spawning memory_pressure.
func memoryPressure() string {
	v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return ""
	}
	switch v {
	case 1:
		return "normal"
	case 2:
		return "warn"
	case 4:
		return "critical"
	}
	return ""
}
