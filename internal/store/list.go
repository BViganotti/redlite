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
func (s *Store) listLocked(sh *shard, key string, now int64, create bool) (*listValue, error) {
	e, ok := s.lookupWrite(sh, key, now)
	if !ok {
		if !create {
			return nil, nil
		}
		lv := newListValue()
		sh.data[key] = s.newEntry(lv, 0)
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
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, create)
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
	s.touch(sh.data[key])
	s.waiters.signal(key)
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
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, false)
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
		delete(sh.data, key)
	} else if len(out) > 0 {
		s.touch(sh.data[key])
	}
	return out, nil
}

// LLen returns the list length.
func (s *Store) LLen(key string) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
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
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
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
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, false)
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
	s.touch(sh.data[key])
	return nil
}

// LRange returns the inclusive range, clamped to the list.
func (s *Store) LRange(key string, start, stop int64) ([][]byte, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
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
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, false)
	if err != nil {
		return err
	}
	if lv == nil {
		return nil
	}
	from, to, ok := normalizeRange(start, stop, lv.len())
	if !ok {
		delete(sh.data, key)
		return nil
	}
	items := make([][]byte, 0, to-from+1)
	for i := from; i <= to; i++ {
		items = append(items, lv.at(i))
	}
	lv.reset(items)
	s.touch(sh.data[key])
	return nil
}

// LRem removes occurrences of value. A positive count starts at the head, a
// negative count at the tail, and zero removes every occurrence.
func (s *Store) LRem(key string, count int64, value []byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, false)
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
		delete(sh.data, key)
		return removed, nil
	}
	lv.reset(kept)
	s.touch(sh.data[key])
	return removed, nil
}

// Move pops one element from source and pushes it to destination in a single
// locked step. It reports false when the source is empty.
func (s *Store) Move(source, destination string, fromLeft, toLeft bool) ([]byte, bool, error) {
	locked := s.lockKeys([]string{source, destination})
	defer s.unlockKeys(locked)

	now := s.now()
	srcShard := s.shardFor(source)
	src, err := s.listLocked(srcShard, source, now, false)
	if err != nil {
		return nil, false, err
	}
	if src == nil || src.len() == 0 {
		return nil, false, nil
	}

	if source == destination {
		var value []byte
		if fromLeft {
			value, _ = src.popFront()
		} else {
			value, _ = src.popBack()
		}
		if toLeft {
			src.pushFront(value)
		} else {
			src.pushBack(value)
		}
		s.touch(srcShard.data[source])
		s.waiters.signal(source)
		return value, true, nil
	}

	// Validate the destination type before mutating anything.
	dstShard := s.shardFor(destination)
	dst, err := s.listLocked(dstShard, destination, now, false)
	if err != nil {
		return nil, false, err
	}

	var value []byte
	if fromLeft {
		value, _ = src.popFront()
	} else {
		value, _ = src.popBack()
	}
	if src.len() == 0 {
		delete(srcShard.data, source)
	} else {
		s.touch(srcShard.data[source])
	}

	if dst == nil {
		dst = newListValue()
		dstShard.data[destination] = s.newEntry(dst, 0)
		s.sets.Add(1)
	}
	if toLeft {
		dst.pushFront(value)
	} else {
		dst.pushBack(value)
	}
	s.touch(dstShard.data[destination])
	s.waiters.signal(destination)
	return value, true, nil
}

// LMPop pops up to count elements from the first non-empty list among keys.
// It returns the key that supplied the elements and the popped values. The
// operation holds every involved shard so it is atomic.
func (s *Store) LMPop(keys []string, left bool, count int) (string, [][]byte, error) {
	if count <= 0 {
		return "", nil, nil
	}
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	for _, key := range keys {
		sh := s.shardFor(key)
		e, ok := s.lookupWrite(sh, key, now)
		if !ok {
			continue
		}
		lv, ok := e.value.(*listValue)
		if !ok {
			return "", nil, ErrWrongType
		}
		if lv.len() == 0 {
			delete(sh.data, key)
			continue
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
			if left {
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
			delete(sh.data, key)
		} else {
			s.touch(sh.data[key])
		}
		return key, out, nil
	}
	return "", nil, nil
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
