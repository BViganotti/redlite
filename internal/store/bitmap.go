package store

import (
	"errors"
	"math"
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

// BitFieldOp is one operation of a BITFIELD command.
type BitFieldOp struct {
	Kind     byte // 'G' GET, 'S' SET, 'I' INCRBY
	Signed   bool
	Bits     int
	Offset   int64
	Value    int64
	Overflow byte // 'W' wrap, 'S' saturate, 'F' fail
}

// BitFieldResult is the reply for one operation. Valid is false for a failed
// INCRBY under OVERFLOW FAIL.
type BitFieldResult struct {
	Value int64
	Valid bool
}

// BitField applies a batch of bitfield operations atomically.
func (s *Store) BitField(key string, ops []BitFieldOp, readOnly bool) ([]BitFieldResult, error) {
	now := s.now()
	write := !readOnly
	for _, op := range ops {
		if op.Kind != 'G' {
			write = true
		}
	}

	var sh *shard
	if write {
		sh = s.lockWrite(key)
	} else {
		sh = s.lockRead(key)
	}
	if write {
		defer s.unlockWrite(sh)
	} else {
		defer s.unlockRead(sh)
	}

	e, ok := s.lookupRead(sh, key, now)
	var data []byte
	if ok {
		sv, isString := e.value.(*stringValue)
		if !isString {
			return nil, ErrWrongType
		}
		if write {
			data = sv.data
		} else {
			data = sv.data
		}
	}

	results := make([]BitFieldResult, len(ops))
	changed := false
	for i, op := range ops {
		// Redis limits the starting offset to proto-max-bulk-len bytes; the
		// field may extend a few bytes past that boundary.
		if op.Offset < 0 || op.Offset/8 >= int64(maxStringSize) {
			return nil, ErrOffsetOutOfRange
		}
		if op.Kind == 'G' {
			results[i] = BitFieldResult{Value: readFieldBits(data, op), Valid: true}
			continue
		}

		old := readFieldBits(data, BitFieldOp{Signed: op.Signed, Bits: op.Bits, Offset: op.Offset})
		results[i] = BitFieldResult{Value: old, Valid: true}

		// Redis grows the string to cover a write field before evaluating
		// overflow, so a FAIL still leaves the (zero-filled) extension behind.
		needed := int((op.Offset+int64(op.Bits)+7)/8) - len(data)
		modified := needed > 0
		if needed > 0 {
			data = append(data, make([]byte, needed)...)
			changed = true
		}

		var raw uint64
		switch op.Kind {
		case 'S':
			value, valid := setFieldValue(op)
			if !valid {
				results[i].Valid = false
				continue
			}
			raw = fieldMask(op.Bits) & uint64(value)
			if value != old {
				modified = true
			}
		case 'I':
			next, valid := addFieldValue(old, op)
			if !valid {
				results[i].Valid = false
				continue
			}
			results[i].Value = next
			raw = fieldMask(op.Bits) & uint64(next)
			if next != old {
				modified = true
			}
		default:
			return nil, errors.New("syntax error")
		}

		if modified {
			writeFieldBits(data, op.Offset, op.Bits, raw)
			changed = true
		}
	}

	if write && changed {
		if ok {
			e.value.(*stringValue).data = data
			s.touch(e)
		} else {
			sh.data[key] = s.newEntry(&stringValue{data: data}, 0)
			s.sets.Add(1)
		}
	}
	return results, nil
}

func fieldMask(bits int) uint64 {
	if bits >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(bits)) - 1
}

func readFieldBits(data []byte, op BitFieldOp) int64 {
	var raw uint64
	for i := 0; i < op.Bits; i++ {
		pos := op.Offset + int64(i)
		index := pos / 8
		if index >= int64(len(data)) {
			continue
		}
		if data[index]&(1<<(7-uint(pos%8))) != 0 {
			raw |= uint64(1) << uint(op.Bits-1-i)
		}
	}
	if op.Signed && op.Bits < 64 && raw&(uint64(1)<<uint(op.Bits-1)) != 0 {
		raw |= ^fieldMask(op.Bits)
	}
	return int64(raw)
}

func writeFieldBits(data []byte, offset int64, bits int, raw uint64) {
	for i := 0; i < bits; i++ {
		pos := offset + int64(i)
		index := pos / 8
		mask := byte(1) << (7 - uint(pos%8))
		if raw&(uint64(1)<<uint(bits-1-i)) != 0 {
			data[index] |= mask
		} else {
			data[index] &^= mask
		}
	}
}

// setFieldValue applies the current overflow mode to a SET value that does
// not fit the field. Valid is false only for OVERFLOW FAIL.
func setFieldValue(op BitFieldOp) (int64, bool) {
	if op.Signed {
		var minValue, maxValue int64
		if op.Bits >= 64 {
			minValue, maxValue = math.MinInt64, math.MaxInt64
		} else {
			maxValue = int64(uint64(1)<<uint(op.Bits-1)) - 1
			minValue = -maxValue - 1
		}
		if op.Value >= minValue && op.Value <= maxValue {
			return op.Value, true
		}
		switch op.Overflow {
		case 'F':
			return 0, false
		case 'S':
			if op.Value < minValue {
				return minValue, true
			}
			return maxValue, true
		default:
			return op.Value, true
		}
	}

	maxValue := int64(fieldMask(op.Bits))
	if op.Value >= 0 && op.Value <= maxValue {
		return op.Value, true
	}
	switch op.Overflow {
	case 'F':
		return 0, false
	case 'S':
		// A negative int64 is cast to uint64 by Redis before this check, so
		// it is out of range on the high side and saturates to the maximum.
		return maxValue, true
	default:
		return op.Value, true
	}
}

// addFieldValue computes the new value of an INCRBY, honouring the overflow
// mode. It returns the new logical value and whether the operation succeeded.
func addFieldValue(current int64, op BitFieldOp) (int64, bool) {
	bits := op.Bits
	if op.Signed {
		var minValue, maxValue int64
		if bits >= 64 {
			minValue, maxValue = math.MinInt64, math.MaxInt64
		} else {
			maxValue = int64(uint64(1)<<uint(bits-1)) - 1
			minValue = -maxValue - 1
		}
		sum := current + op.Value
		wrapped := (op.Value > 0 && sum < current) || (op.Value < 0 && sum > current)
		overflow := wrapped || sum < minValue || sum > maxValue
		if !overflow {
			return sum, true
		}
		switch op.Overflow {
		case 'F':
			return 0, false
		case 'S':
			if op.Value > 0 {
				return maxValue, true
			}
			return minValue, true
		default:
			raw := uint64(current) + uint64(op.Value)
			if bits < 64 {
				raw &= fieldMask(bits)
				if raw&(uint64(1)<<uint(bits-1)) != 0 {
					return int64(raw | ^fieldMask(bits)), true
				}
			}
			return int64(raw), true
		}
	}

	maxValue := fieldMask(bits)
	currentRaw := uint64(current)
	var under, over bool
	var next uint64
	if op.Value >= 0 {
		delta := uint64(op.Value)
		over = delta > maxValue-currentRaw
		next = currentRaw + delta
	} else {
		magnitude := uint64(-(op.Value + 1)) + 1
		under = magnitude > currentRaw
		next = currentRaw - magnitude
	}

	switch {
	case over || under:
		switch op.Overflow {
		case 'F':
			return 0, false
		case 'S':
			if over {
				return int64(maxValue), true
			}
			return 0, true
		default:
			next &= maxValue
		}
	default:
		next &= maxValue
	}
	return int64(next), true
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
