package artifactbrand

import (
	"math"
	"strconv"
)

// luminance is the WCAG relative luminance of a #rrggbb colour.
func luminance(hex string) float64 {
	ch := func(i int) float64 {
		n, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		c := float64(n) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(1) + 0.7152*ch(3) + 0.0722*ch(5)
}

// contrast is the WCAG contrast ratio of two #rrggbb colours, 1 to 21.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
