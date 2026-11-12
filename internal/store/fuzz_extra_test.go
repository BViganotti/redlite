package store

import "testing"

// FuzzRestore checks that no DUMP-shaped payload can panic the RESTORE parser.
func FuzzRestore(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x00, 0x09, 0x00})
	f.Add([]byte{
		0x00, 0x05, 'h', 'e', 'l', 'l', 'o',
		0x0e, 0x00,
		0x11, 0x29, 0x07, 0x09, 0x36, 0x07, 0x34, 0x7b,
	})
	f.Fuzz(func(t *testing.T, payload []byte) {
		s, _ := newTestStore()
		_ = s.Restore("k", payload, 0, true)
	})
}

// FuzzLexRange checks the lexicographic bound parser.
func FuzzLexRange(f *testing.F) {
	f.Add([]byte("-"), []byte("+"))
	f.Add([]byte("[a"), []byte("(z"))
	f.Add([]byte("bad"), []byte("+"))
	f.Fuzz(func(t *testing.T, min, max []byte) {
		r, err := ParseLexRange(min, max)
		if err != nil {
			return
		}
		_ = lexAtLeast("m", r.Min)
		_ = lexAtMost("m", r.Max)
	})
}
