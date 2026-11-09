package store

import (
	"math"
	"strconv"
)

// FieldValue is one hash field, used by HGETALL and HSCAN.
type FieldValue struct {
	Field string
	Value []byte
}

// hashLocked returns the hash at key, creating it when create is true. A nil
// hash with a nil error means the key does not exist.
func (s *Store) hashLocked(key string, now int64, create bool) (*hashValue, error) {
	e, ok := s.lookupWrite(key, now)
	if !ok {
		if !create {
			return nil, nil
		}
		hv := &hashValue{fields: make(map[string][]byte)}
		s.data[key] = &entry{value: hv}
		s.sets.Add(1)
		return hv, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, ErrWrongType
	}
	return hv, nil
}

// HSet sets one or more fields and reports how many were new.
func (s *Store) HSet(key string, pairs []KeyValue) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	hv, err := s.hashLocked(key, now, true)
	if err != nil {
		return 0, err
	}
	var added int64
	for _, pair := range pairs {
		if _, exists := hv.fields[pair.Key]; !exists {
			added++
		}
		hv.fields[pair.Key] = clone(pair.Value)
	}
	if len(pairs) > 0 {
		s.stampKey(key)
	}
	return added, nil
}

// HSetNX sets a field only when it does not exist.
func (s *Store) HSetNX(key, field string, value []byte) (bool, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	hv, err := s.hashLocked(key, now, true)
	if err != nil {
		return false, err
	}
	if _, exists := hv.fields[field]; exists {
		return false, nil
	}
	hv.fields[field] = clone(value)
	s.stampKey(key)
	return true, nil
}

// HGet returns a copy of one field.
func (s *Store) HGet(key, field string) ([]byte, bool, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, false, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, false, ErrWrongType
	}
	value, ok := hv.fields[field]
	if !ok {
		return nil, false, nil
	}
	return clone(value), true, nil
}

// HDel removes fields and reports how many existed.
func (s *Store) HDel(key string, fields []string) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	hv, err := s.hashLocked(key, now, false)
	if err != nil {
		return 0, err
	}
	if hv == nil {
		return 0, nil
	}

	var removed int64
	for _, field := range fields {
		if _, ok := hv.fields[field]; ok {
			delete(hv.fields, field)
			removed++
		}
	}
	if len(hv.fields) == 0 {
		delete(s.data, key)
	} else if removed > 0 {
		s.stampKey(key)
	}
	return removed, nil
}

// HLen returns the number of fields.
func (s *Store) HLen(key string) (int64, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return 0, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return 0, ErrWrongType
	}
	return int64(len(hv.fields)), nil
}

// HExists reports whether a field is present.
func (s *Store) HExists(key, field string) (bool, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return false, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return false, ErrWrongType
	}
	_, exists := hv.fields[field]
	return exists, nil
}

// HGetAll returns every field, sorted by name so replies are deterministic.
func (s *Store) HGetAll(key string) ([]FieldValue, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, ErrWrongType
	}
	return hashPairs(hv.fields, sortedKeys(hv.fields)), nil
}

// HKeys returns every field name, sorted.
func (s *Store) HKeys(key string) ([]string, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, ErrWrongType
	}
	return sortedKeys(hv.fields), nil
}

// HVals returns every field value, ordered by field name.
func (s *Store) HVals(key string) ([][]byte, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, ErrWrongType
	}
	fields := sortedKeys(hv.fields)
	out := make([][]byte, 0, len(fields))
	for _, field := range fields {
		out = append(out, clone(hv.fields[field]))
	}
	return out, nil
}

// HIncrBy adds delta to an integer field, initializing missing fields to zero.
func (s *Store) HIncrBy(key, field string, delta int64) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	hv, err := s.hashLocked(key, now, true)
	if err != nil {
		return 0, err
	}

	var current int64
	if raw, ok := hv.fields[field]; ok {
		parsed, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return 0, ErrNotInteger
		}
		current = parsed
	}
	if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
		return 0, ErrOverflow
	}

	next := current + delta
	hv.fields[field] = strconv.AppendInt(nil, next, 10)
	s.stampKey(key)
	return next, nil
}

// HScan walks a hash from cursor, returning the next cursor and matching
// fields. Like SCAN, the cursor indexes a sorted snapshot.
func (s *Store) HScan(key string, cursor uint64, pattern string, count int) (uint64, []FieldValue, error) {
	if count <= 0 {
		count = 10
	}
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return 0, nil, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return 0, nil, ErrWrongType
	}
	fields := sortedKeys(hv.fields)
	if cursor >= uint64(len(fields)) {
		return 0, nil, nil
	}

	var (
		out      []FieldValue
		examined int
	)
	for examined < count && cursor < uint64(len(fields)) {
		field := fields[cursor]
		cursor++
		examined++
		if pattern != "" && pattern != "*" && !Match(pattern, field) {
			continue
		}
		out = append(out, FieldValue{Field: field, Value: clone(hv.fields[field])})
	}
	if cursor >= uint64(len(fields)) {
		cursor = 0
	}
	return cursor, out, nil
}

func hashPairs(fields map[string][]byte, order []string) []FieldValue {
	out := make([]FieldValue, 0, len(order))
	for _, field := range order {
		out = append(out, FieldValue{Field: field, Value: clone(fields[field])})
	}
	return out
}
