package textsafe

import "testing"

func TestUnsafeSet(t *testing.T) {
	for _, r := range []rune{0, '\n', 0x7f, 0x85, 0xad, 0x61c, 0x200b, 0x202e, 0x2066, 0xfeff, 0xfff9, 0xfffb, 0xe0000, 0xe0041, 0xe007f} {
		if !Unsafe(r) {
			t.Errorf("U+%04X passes", r)
		}
	}
	for _, r := range []rune{' ', 'a', 'é', '↳', '😀', 0xfffc, 0xe0080} {
		if Unsafe(r) {
			t.Errorf("U+%04X refused", r)
		}
	}
}
