package store

import "errors"

// Errors returned by LCS.
var (
	ErrLCSWrongType = errors.New("The specified keys must contain string values")
	ErrLCSTooBig    = errors.New("Insufficient memory, transient memory for LCS exceeds proto-max-bulk-len")
)

// LCSMatch is one contiguous match reported by LCS IDX.
type LCSMatch struct {
	AStart, AEnd int64
	BStart, BEnd int64
	Length       int64
}

// LCSResult is the outcome of an LCS computation.
type LCSResult struct {
	Length   int64
	Sequence []byte
	Matches  []LCSMatch
}

// LCS computes the longest common subsequence of the strings at two keys. The
// dynamic-programming table and backtracking order mirror Redis exactly, so
// the returned subsequence and match ranges are identical for any input.
func (s *Store) LCS(keyA, keyB string, getLen, getIdx bool, minMatchLen int64) (LCSResult, error) {
	locked := s.lockKeys([]string{keyA, keyB})
	defer s.unlockKeys(locked)

	now := s.now()
	a, err := s.lcsString(keyA, now)
	if err != nil {
		return LCSResult{}, err
	}
	b, err := s.lcsString(keyB, now)
	if err != nil {
		return LCSResult{}, err
	}

	alen, blen := len(a), len(b)
	cells := uint64(alen+1) * uint64(blen+1)
	if cells > uint64(maxStringSize)/4 {
		return LCSResult{}, ErrLCSTooBig
	}
	// The table stores uint32 lengths; index as table[i*(blen+1)+j].
	table := make([]uint32, cells)
	width := blen + 1
	for i := 1; i <= alen; i++ {
		for j := 1; j <= blen; j++ {
			switch {
			case a[i-1] == b[j-1]:
				table[i*width+j] = table[(i-1)*width+j-1] + 1
			default:
				left := table[(i-1)*width+j]
				up := table[i*width+j-1]
				if left > up {
					table[i*width+j] = left
				} else {
					table[i*width+j] = up
				}
			}
		}
	}

	length := int64(table[alen*width+blen])
	computeLCS := getIdx || !getLen
	var (
		result      []byte
		idx         int
		matches     []LCSMatch
		arangeStart int = alen
		arangeEnd   int
		brangeStart int
		brangeEnd   int
		minMatch    = minMatchLen
	)
	if computeLCS {
		result = make([]byte, length)
		idx = int(length)
	}
	if minMatch < 0 {
		minMatch = 0
	}

	i, j := alen, blen
	for computeLCS && i > 0 && j > 0 {
		emitRange := false
		if a[i-1] == b[j-1] {
			result[idx-1] = a[i-1]
			if arangeStart == alen {
				arangeStart = i - 1
				arangeEnd = i - 1
				brangeStart = j - 1
				brangeEnd = j - 1
			} else {
				if arangeStart == i && brangeStart == j {
					arangeStart--
					brangeStart--
				} else {
					emitRange = true
				}
			}
			if arangeStart == 0 || brangeStart == 0 {
				emitRange = true
			}
			idx--
			i--
			j--
		} else {
			left := table[(i-1)*width+j]
			up := table[i*width+j-1]
			if left > up {
				i--
			} else {
				j--
			}
			if arangeStart != alen {
				emitRange = true
			}
		}

		matchLen := int64(arangeEnd - arangeStart + 1)
		if emitRange {
			if minMatch == 0 || matchLen >= minMatch {
				matches = append(matches, LCSMatch{
					AStart: int64(arangeStart),
					AEnd:   int64(arangeEnd),
					BStart: int64(brangeStart),
					BEnd:   int64(brangeEnd),
					Length: matchLen,
				})
			}
			arangeStart = alen
		}
	}
	return LCSResult{Length: length, Sequence: result, Matches: matches}, nil
}

func (s *Store) lcsString(key string, now int64) ([]byte, error) {
	e, ok := s.lookupRead(s.shardFor(key), key, now)
	if !ok {
		return nil, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return nil, ErrLCSWrongType
	}
	return sv.data, nil
}
