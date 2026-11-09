package store

import "testing"

// FuzzMatch checks the glob matcher for panics and out-of-range indexing.
func FuzzMatch(f *testing.F) {
	seeds := [][2]string{
		{"*", "anything"},
		{"user:*", "user:1"},
		{"a*[b-c]?", "abc"},
		{"[^a]", "b"},
		{`\*`, "*"},
		{"[", "["},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, pattern, name string) {
		_ = Match(pattern, name)
	})
}
