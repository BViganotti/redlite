package store

import (
	"errors"
	"math/bits"
)

// SetBit sets or clears one bit and returns the previous value.
func (s *Store) SetBit(key string, offset int64, bit int) (int64, error) {
	if offset < 0 || offset >= maxStringSize*8 {
		return 0, ErrOffsetOutOfRange
	}

	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	e, exists := s.lookupWrite(sh, key, now)
	var sv *stringValue
	if exists {
		current, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrWrongType
		}
		sv = current
	}

	byteIndex := int(offset / 8)
	bitIndex := uint(offset % 8)
	data := []byte(nil)
	if sv != nil {
		data = sv.data
	}
	if byteIndex >= len(data) {
		data = append(data, make([]byte, byteIndex-len(data)+1)...)
	}

	mask := byte(1) << (7 - bitIndex)
	old := int64(0)
	if data[byteIndex]&mask != 0 {
		old = 1
	}
	if bit == 1 {
		data[byteIndex] |= mask
	} else {
		data[byteIndex] &^= mask
	}

	if sv == nil {
		sh.data[key] = s.newEntry(&stringValue{data: data}, 0)
		s.sets.Add(1)
	} else {
		sv.data = data
		s.touch(e)
	}
	return old, nil
}

// GetBit returns one bit, treating a missing key as zero.
func (s *Store) GetBit(key string, offset int64) (int64, error) {
	if offset < 0 {
		return 0, ErrOffsetOutOfRange
	}

	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}

	byteIndex := int(offset / 8)
	if byteIndex >= len(sv.data) {
		return 0, nil
	}
	if sv.data[byteIndex]&(1<<(7-uint(offset%8))) != 0 {
		return 1, nil
	}
	return 0, nil
}

// BitCount counts set bits in the byte range [start, end]. Negative indexes
// count from the end. hasRange false means the whole value.
func (s *Store) BitCount(key string, start, end int64, hasRange bool) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}

	from, to, ok := normalizeRange(start, end, len(sv.data))
	if !hasRange {
		from, to, ok = 0, len(sv.data)-1, len(sv.data) > 0
	}
	if !ok {
		return 0, nil
	}

	var count int64
	for _, b := range sv.data[from : to+1] {
		count += int64(bits.OnesCount8(b))
	}
	return count, nil
}

// BitPos returns the position of the first bit with the given value.
func (s *Store) BitPos(key string, bit int, start, end int64, hasRange bool) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		if bit == 0 {
			return 0, nil
		}
		return -1, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}
	if len(sv.data) == 0 {
		if bit == 0 {
			return 0, nil
		}
		return -1, nil
	}

	from, to := 0, len(sv.data)-1
	if hasRange {
		var valid bool
		from, to, valid = normalizeRange(start, end, len(sv.data))
		if !valid {
			return -1, nil
		}
	}

	for index := from; index <= to; index++ {
		b := sv.data[index]
		for offset := 0; offset < 8; offset++ {
			v := int64(b>>(7-uint(offset))) & 1
			if v == int64(bit) {
				return int64(index*8 + offset), nil
			}
		}
	}

	// Redis returns the first bit past the end for a zero search in an
	// all-ones string without an explicit range.
	if bit == 0 && !hasRange {
		return int64(len(sv.data) * 8), nil
	}
	return -1, nil
}

// BitOp applies AND, OR, XOR or NOT across the source keys and stores the
// result at destination. It returns the length of the result.
func (s *Store) BitOp(op string, destination string, sources []string) (int64, error) {
	if op != "AND" && op != "OR" && op != "XOR" && op != "NOT" {
		return 0, errors.New("syntax error")
	}
	if op == "NOT" && len(sources) != 1 {
		return 0, errors.New("BITOP NOT must be called with a single source key")
	}

	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	values := make([][]byte, len(sources))
	maxLen := 0
	for i, key := range sources {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			continue
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrWrongType
		}
		values[i] = sv.data
		if len(sv.data) > maxLen {
			maxLen = len(sv.data)
		}
	}

	var result []byte
	if op == "NOT" {
		result = make([]byte, maxLen)
		for i := 0; i < maxLen; i++ {
			result[i] = ^values[0][i]
		}
	} else if maxLen > 0 {
		result = make([]byte, maxLen)
		if op == "AND" {
			for i := 0; i < maxLen; i++ {
				b := byte(0xff)
				for _, value := range values {
					if i < len(value) {
						b &= value[i]
					} else {
						b = 0
						break
					}
				}
				result[i] = b
			}
		} else {
			for i := 0; i < maxLen; i++ {
				var b byte
				for _, value := range values {
					if i < len(value) {
						if op == "OR" {
							b |= value[i]
						} else {
							b ^= value[i]
						}
					}
				}
				result[i] = b
			}
		}
	}

	destShard := s.shardFor(destination)
	if len(result) == 0 {
		delete(destShard.data, destination)
		return 0, nil
	}
	destShard.data[destination] = s.newEntry(&stringValue{data: result}, 0)
	s.sets.Add(1)
	return int64(len(result)), nil
}

// BitCountBits counts set bits in a bit-indexed range.
func (s *Store) BitCountBits(key string, start, end int64) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}

	total := int64(len(sv.data) * 8)
	if start < 0 {
		start += total
	}
	if end < 0 {
		end += total
	}
	if start < 0 {
		start = 0
	}
	if end >= total {
		end = total - 1
	}
	if start > end || start >= total {
		return 0, nil
	}

	var count int64
	for bit := start; bit <= end; bit++ {
		if sv.data[bit/8]&(1<<(7-uint(bit%8))) != 0 {
			count++
		}
	}
	return count, nil
}
