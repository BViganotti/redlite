package store

import (
	"errors"
	"math/big"
	"strings"
)

// Errors for the extended string commands.
var (
	ErrOffsetOutOfRange = errors.New("offset is out of range")
	ErrStringTooLarge   = errors.New("string exceeds maximum allowed size")
	ErrNotFloat         = errors.New("value is not a valid float")
	ErrFloatResult      = errors.New("increment would produce NaN or Infinity")
)

const maxStringSize = 512 * 1024 * 1024

// GetRange returns the inclusive byte range [start, end] with Redis' negative
// index rules.
func (s *Store) GetRange(key string, start, end int64) ([]byte, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return nil, ErrWrongType
	}

	length := int64(len(sv.data))
	if length == 0 {
		return nil, nil
	}
	if start < 0 {
		start += length
	}
	if end < 0 {
		end += length
	}
	if start < 0 {
		start = 0
	}
	if end >= length {
		end = length - 1
	}
	if start > end || start >= length || end < 0 {
		return nil, nil
	}
	return clone(sv.data[start : end+1]), nil
}

// SetRange overwrites bytes starting at offset, zero-padding any gap, and
// returns the resulting length.
func (s *Store) SetRange(key string, offset int64, value []byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	e, ok := s.lookupWrite(sh, key, now)
	var sv *stringValue
	if ok {
		current, isString := e.value.(*stringValue)
		if !isString {
			return 0, ErrWrongType
		}
		sv = current
	}

	data := []byte(nil)
	if sv != nil {
		data = sv.data
	}
	if len(value) == 0 {
		return int64(len(data)), nil
	}
	if offset < 0 {
		return 0, ErrOffsetOutOfRange
	}
	if offset > int64(maxStringSize-len(value)) {
		return 0, ErrStringTooLarge
	}

	end := int(offset) + len(value)
	if end > len(data) {
		data = append(data, make([]byte, end-len(data))...)
	}
	copy(data[offset:], value)

	if sv == nil {
		sh.data[key] = s.newEntry(&stringValue{data: data}, 0)
		s.sets.Add(1)
	} else {
		sv.data = data
		s.touch(e)
	}
	return int64(len(data)), nil
}

// IncrByFloat adds a decimal increment to a string value using 64-bit-mantissa
// arithmetic, mirroring Redis' long double behaviour, and stores the formatted
// result.
func (s *Store) IncrByFloat(key, increment string) (string, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	e, ok := s.lookupWrite(sh, key, now)
	current := "0"
	var sv *stringValue
	if ok {
		existing, isString := e.value.(*stringValue)
		if !isString {
			return "", ErrWrongType
		}
		sv = existing
		current = string(existing.data)
	}

	result, err := addDecimalStrings(current, increment)
	if err != nil {
		return "", err
	}

	if sv == nil {
		sh.data[key] = s.newEntry(&stringValue{data: []byte(result)}, 0)
		s.sets.Add(1)
	} else {
		sv.data = []byte(result)
		s.touch(e)
	}
	return result, nil
}

func addDecimalStrings(a, b string) (string, error) {
	left, _, err := big.ParseFloat(a, 10, 64, big.ToNearestEven)
	if err != nil {
		return "", ErrNotFloat
	}
	right, _, err := big.ParseFloat(b, 10, 64, big.ToNearestEven)
	if err != nil {
		return "", ErrNotFloat
	}
	if left.IsInf() || right.IsInf() {
		return "", ErrFloatResult
	}

	sum := new(big.Float).SetPrec(64).Add(left, right)
	if sum.IsInf() {
		return "", ErrFloatResult
	}

	out := sum.Text('f', 17)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(out, "0")
		out = strings.TrimRight(out, ".")
	}
	if out == "" || out == "-0" || out == "-" {
		out = "0"
	}
	return out, nil
}
