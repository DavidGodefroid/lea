// Package textsafe is the one definition of the characters lea never lets through in
// agent-controlled text: controls and characters that hide or reorder what a reader sees.
package textsafe

// Control reports C0, DEL and C1 control characters.
func Control(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) }

// Invisible reports characters that are drawn as nothing or reorder text: soft hyphen, Arabic
// letter mark, zero-width and bidi controls, line/paragraph separators, word joiners and
// invisible operators, bidi isolates, BOM, interlinear annotation marks and the tag block.
func Invisible(r rune) bool {
	return r == 0x00ad || r == 0x061c || (r >= 0x200b && r <= 0x200f) || (r >= 0x2028 && r <= 0x202e) ||
		(r >= 0x2060 && r <= 0x2064) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff ||
		(r >= 0xfff9 && r <= 0xfffb) || (r >= 0xe0000 && r <= 0xe007f)
}

// Unsafe is Control or Invisible.
func Unsafe(r rune) bool { return Control(r) || Invisible(r) }
