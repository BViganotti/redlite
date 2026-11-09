package store

import (
	"errors"
	"math"
)

// Errors specific to list commands.
var (
	ErrNoSuchKey       = errors.New("no such key")
	ErrIndexOutOfRange = errors.New("index out of range")
)

// listLocked returns the list at key, creating it when create is true.
func (s *Store) listLocked(key string, now int64, create bool) (*listValue, error) {
	e, ok := s.lookupWrite(key, now)
	if !ok {
		if !create {
			return nil, nil
		}
		lv := newListValue()
		s.data[key] = &entry{value: lv}
		s.sets.Add(1)
		return lv, nil
	}
	lv, ok := e.value.(*listValue)
	if !ok {
		return nil, ErrWrongType
	}
	return lv, nil
}

// LPush prepends values, one at a time, and returns the new length.
func (s *Store) LPush(key string, values [][]byte) (int64, error) {
	return s.push(key, values, true, true)
}

// RPush appends values and returns the new length.
func (s *Store) RPush(key string, values [][]byte) (int64, error) {
	return s.push(key, values, false, true)
}

// LPushX prepends to an existing list only.
func (s *Store) LPushX(key string, values [][]byte) (int64, error) {
	return s.push(key, values, true, false)
}

// RPushX appends to an existing list only.
func (s *Store) RPushX(key string, values [][]byte) (int64, error) {
	return s.push(key, values, false, false)
}

func (s *Store) push(key string, values [][]byte, front, create bool) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv, err := s.listLocked(key, now, create)
	if err != nil {
		return 0, err
	}
	if lv == nil {
		return 0, nil
	}
	for _, value := range values {
		if front {
			lv.pushFront(clone(value))
		} else {
			lv.pushBack(clone(value))
		}
	}
	s.stampKey(key)
	return int64(lv.len()), nil
}

// LPop removes up to count elements from the head. A missing key yields an
// empty result.
func (s *Store) LPop(key string, count int) ([][]byte, error) {
	return s.pop(key, count, true)
}

// RPop removes up to count elements from the tail.
func (s *Store) RPop(key string, count int) ([][]byte, error) {
	return s.pop(key, count, false)
}

func (s *Store) pop(key string, count int, front bool) ([][]byte, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv, err := s.listLocked(key, now, false)
	if err != nil {
		return nil, err
	}
	if lv == nil || count <= 0 {
		return nil, nil
	}

	limit := count
	if limit > lv.len() {
		limit = lv.len()
	}
	out := make([][]byte, 0, limit)
	for i := 0; i < limit; i++ {
		var (
			value []byte
			ok    bool
		)
		if front {
			value, ok = lv.popFront()
		} else {
			value, ok = lv.popBack()
		}
		if !ok {
			break
		}
		out = append(out, value)
	}
	if lv.len() == 0 {
		delete(s.data, key)
	} else if len(out) > 0 {
		s.stampKey(key)
	}
	return out, nil
}

// LLen returns the list length.
func (s *Store) LLen(key string) (int64, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return 0, nil
	}
	lv, ok := e.value.(*listValue)
	if !ok {
		return 0, ErrWrongType
	}
	return int64(lv.len()), nil
}

// LIndex returns one element, or false when the index is out of range.
func (s *Store) LIndex(key string, index int64) ([]byte, bool, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, false, nil
	}
	lv, ok := e.value.(*listValue)
	if !ok {
		return nil, false, ErrWrongType
	}
	pos, ok := normalizeIndex(index, lv.len())
	if !ok {
		return nil, false, nil
	}
	return clone(lv.at(pos)), true, nil
}

// LSet replaces one element.
func (s *Store) LSet(key string, index int64, value []byte) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv, err := s.listLocked(key, now, false)
	if err != nil {
		return err
	}
	if lv == nil {
		return ErrNoSuchKey
	}
	pos, ok := normalizeIndex(index, lv.len())
	if !ok {
		return ErrIndexOutOfRange
	}
	lv.set(pos, clone(value))
	s.stampKey(key)
	return nil
}

// LRange returns the inclusive range, clamped to the list.
func (s *Store) LRange(key string, start, stop int64) ([][]byte, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		return nil, nil
	}
	lv, ok := e.value.(*listValue)
	if !ok {
		return nil, ErrWrongType
	}
	from, to, ok := normalizeRange(start, stop, lv.len())
	if !ok {
		return nil, nil
	}
	out := make([][]byte, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, clone(lv.at(i)))
	}
	return out, nil
}

// LTrim keeps only the inclusive range, deleting the key when nothing remains.
func (s *Store) LTrim(key string, start, stop int64) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv, err := s.listLocked(key, now, false)
	if err != nil {
		return err
	}
	if lv == nil {
		return nil
	}
	from, to, ok := normalizeRange(start, stop, lv.len())
	if !ok {
		delete(s.data, key)
		return nil
	}
	items := make([][]byte, 0, to-from+1)
	for i := from; i <= to; i++ {
		items = append(items, lv.at(i))
	}
	lv.reset(items)
	s.stampKey(key)
	return nil
}

// LRem removes occurrences of value. A positive count starts at the head, a
// negative count at the tail, and zero removes every occurrence.
func (s *Store) LRem(key string, count int64, value []byte) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv, err := s.listLocked(key, now, false)
	if err != nil {
		return 0, err
	}
	if lv == nil || lv.len() == 0 {
		return 0, nil
	}

	target := string(value)
	items := lv.toSlice()
	remove := make([]bool, len(items))
	var removed int64

	if count >= 0 {
		limit := count
		if limit == 0 {
			limit = int64(len(items))
		}
		for i, item := range items {
			if removed >= limit {
				break
			}
			if string(item) == target {
				remove[i] = true
				removed++
			}
		}
	} else {
		limit := -count
		if count == math.MinInt64 {
			limit = math.MaxInt64
		}
		for i := len(items) - 1; i >= 0 && removed < limit; i-- {
			if string(items[i]) == target {
				remove[i] = true
				removed++
			}
		}
	}

	if removed == 0 {
		return 0, nil
	}

	kept := make([][]byte, 0, len(items)-int(removed))
	for i, item := range items {
		if !remove[i] {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(s.data, key)
		return removed, nil
	}
	lv.reset(kept)
	s.stampKey(key)
	return removed, nil
}

func normalizeIndex(index int64, length int) (int, bool) {
	if index < 0 {
		index += int64(length)
	}
	if index < 0 || index >= int64(length) {
		return 0, false
	}
	return int(index), true
}

func normalizeRange(start, stop int64, length int) (int, int, bool) {
	if length == 0 {
		return 0, 0, false
	}
	n := int64(length)
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	if start > stop || start >= n {
		return 0, 0, false
	}
	return int(start), int(stop), true
}
