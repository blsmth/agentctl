package improve

import "testing"

func TestStripInvisible(t *testing.T) {
	r := func(cps ...rune) string { return string(cps) }
	tests := []struct{ name, in, want string }{
		{"plain", "hello\n\tworld", "hello\n\tworld"},
		{"zero width", "a" + r(0x200B) + "b" + r(0x200C) + "c" + r(0x200D) + "d" + r(0x2060) + "e" + r(0xFEFF), "abcde"},
		{"braille padding", r(0x2800, 0x2800) + "status" + r(0x2800), "status"},
		{"soft hyphen and bidi", "co" + r(0xAD) + "op " + r(0x202E) + "end" + r(0x202C), "coop end"},
		{"hangul filler", "x" + r(0x3164) + "y", "xy"},
		{"visible unicode kept", "caf" + r(0xE9) + " " + r(0x65E5, 0x672C) + " " + r(0x2713), "caf" + r(0xE9) + " " + r(0x65E5, 0x672C) + " " + r(0x2713)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripInvisible(tt.in); got != tt.want {
				t.Errorf("stripInvisible(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
