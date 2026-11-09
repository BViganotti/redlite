package store

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"user:*", "user:1", true},
		{"user:*", "admin:1", false},
		{"user:?", "user:1", true},
		{"user:?", "user:12", false},
		{"user:[abc]", "user:b", true},
		{"user:[abc]", "user:d", false},
		{"user:[a-c]", "user:c", true},
		{"user:[^a-c]", "user:d", true},
		{"user:[^a-c]", "user:a", false},
		{"user:[!a-c]", "user:d", true},
		{`user:\*`, "user:*", true},
		{`user:\*`, "user:1", false},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"", "", true},
		{"", "x", false},
		{"foo", "foo", true},
		{"foo", "foobar", false},
		{"[", "[", true},
	}
	for _, tc := range tests {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
