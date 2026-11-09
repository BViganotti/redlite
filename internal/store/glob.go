package store

// Match reports whether name matches a Redis glob pattern. Supported syntax:
//
//   - any sequence of bytes
//     ?      exactly one byte
//     [abc]  one byte from the set; [^abc] and [!abc] negate; [a-z] is a range
//     \x     the literal byte x
func Match(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	p, n := 0, 0
	star, starName := -1, 0

	for n < len(name) {
		if p < len(pattern) {
			switch pattern[p] {
			case '*':
				star = p
				starName = n
				p++
				continue
			case '?':
				p++
				n++
				continue
			case '[':
				if ok, next := matchClass(pattern, p, name[n]); ok {
					p = next
					n++
					continue
				}
			case '\\':
				if p+1 < len(pattern) && pattern[p+1] == name[n] {
					p += 2
					n++
					continue
				}
				if p+1 == len(pattern) && name[n] == '\\' {
					p++
					n++
					continue
				}
			default:
				if pattern[p] == name[n] {
					p++
					n++
					continue
				}
			}
		}

		if star >= 0 {
			starName++
			n = starName
			p = star + 1
			continue
		}
		return false
	}

	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchClass matches one character class starting at pattern[start] == '[' and
// returns whether it matched plus the index just past the class.
func matchClass(pattern string, start int, ch byte) (bool, int) {
	i := start + 1
	if i >= len(pattern) {
		return ch == '[', start + 1
	}

	negate := false
	if pattern[i] == '^' || pattern[i] == '!' {
		negate = true
		i++
	}

	matched := false
	first := true
	for i < len(pattern) {
		if pattern[i] == ']' && !first {
			if negate {
				matched = !matched
			}
			return matched, i + 1
		}
		first = false

		lo := pattern[i]
		if lo == '\\' && i+1 < len(pattern) {
			i++
			lo = pattern[i]
		}
		i++

		if i+1 < len(pattern) && pattern[i] == '-' && pattern[i+1] != ']' {
			hi := pattern[i+1]
			if hi == '\\' && i+2 < len(pattern) {
				hi = pattern[i+2]
				i++
			}
			i += 2
			if lo <= ch && ch <= hi {
				matched = true
			}
			continue
		}
		if lo == ch {
			matched = true
		}
	}

	// Unterminated class: Redis treats the '[' as a literal.
	return ch == '[', start + 1
}
