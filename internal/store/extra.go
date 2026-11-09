package store

import (
	"math/rand/v2"
	"sort"
	"strconv"
)

// ZSetOp computes a union, intersection or difference of sorted sets and
// optionally stores the result at destination.
func (s *Store) ZSetOp(op string, keys []string, weights []float64, aggregate string, destination string) ([]ScoreMember, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	if len(weights) == 0 {
		weights = make([]float64, len(keys))
		for i := range weights {
			weights[i] = 1
		}
	}
	now := s.now()

	sets := make([]map[string]float64, len(keys))
	for i, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			sets[i] = map[string]float64{}
			continue
		}
		zv, ok := e.value.(*zsetValue)
		if !ok {
			return nil, ErrWrongType
		}
		sets[i] = zv.dict
	}

	result := make(map[string]float64)
	combine := func(current float64, exists bool, score float64) float64 {
		if !exists {
			return score
		}
		switch aggregate {
		case "MIN":
			if score < current {
				return score
			}
		case "MAX":
			if score > current {
				return score
			}
		default:
			return current + score
		}
		return current
	}

	switch op {
	case "UNION":
		for i, set := range sets {
			for member, score := range set {
				weighted := score * weights[i]
				if existing, ok := result[member]; ok {
					result[member] = combine(existing, true, weighted)
				} else {
					result[member] = weighted
				}
			}
		}

	case "INTER":
		if len(sets) == 0 {
			break
		}
		for member, score := range sets[0] {
			value := score * weights[0]
			present := true
			for i := 1; i < len(sets); i++ {
				other, ok := sets[i][member]
				if !ok {
					present = false
					break
				}
				value = combine(value, true, other*weights[i])
			}
			if present {
				result[member] = value
			}
		}

	case "DIFF":
		if len(sets) == 0 {
			break
		}
		for member, score := range sets[0] {
			keep := true
			for i := 1; i < len(sets); i++ {
				if _, ok := sets[i][member]; ok {
					keep = false
					break
				}
			}
			if keep {
				result[member] = score * weights[0]
			}
		}

	default:
		return nil, ErrWrongType
	}

	entries := make([]ScoreMember, 0, len(result))
	for member, score := range result {
		entries = append(entries, ScoreMember{Score: score, Member: member})
	}
	sort.Slice(entries, func(i, j int) bool {
		return less(entries[i].Score, entries[i].Member, entries[j].Score, entries[j].Member)
	})

	if destination != "" {
		destShard := s.shardFor(destination)
		if len(entries) == 0 {
			delete(destShard.data, destination)
			return entries, nil
		}
		zv := newZSetValue()
		for _, entry := range entries {
			zv.dict[entry.Member] = entry.Score
			zv.list.insert(entry.Member, entry.Score)
		}
		destShard.data[destination] = s.newEntry(zv, 0)
		s.sets.Add(1)
	}
	return entries, nil
}

// ZPop removes up to count members from the low or high end.
func (s *Store) ZPop(key string, count int, min bool) ([]ScoreMember, error) {
	if count <= 0 {
		return nil, nil
	}
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	zv, err := s.zsetLocked(sh, key, now, false)
	if err != nil {
		return nil, err
	}
	if zv == nil {
		return nil, nil
	}
	if count > zv.list.length {
		count = zv.list.length
	}

	out := make([]ScoreMember, 0, count)
	for i := 0; i < count; i++ {
		var node *skipNode
		if min {
			node = zv.list.nodeByRank(0)
		} else {
			node = zv.list.nodeByRank(zv.list.length - 1)
		}
		if node == nil {
			break
		}
		out = append(out, ScoreMember{Score: node.score, Member: node.member})
		delete(zv.dict, node.member)
		zv.list.delete(node.member, node.score)
	}
	if len(zv.dict) == 0 {
		delete(sh.data, key)
	} else if len(out) > 0 {
		s.touch(sh.data[key])
	}
	return out, nil
}

// ZRandMember returns random members without removing them. A negative count
// allows repeats.
func (s *Store) ZRandMember(key string, count int) ([]ScoreMember, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return nil, ErrWrongType
	}
	all := zv.list.all()
	if count == 0 || len(all) == 0 {
		return nil, nil
	}

	if count < 0 {
		n := -count
		if n < 0 {
			n = len(all)
		}
		out := make([]ScoreMember, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, all[rand.IntN(len(all))])
		}
		return out, nil
	}
	if count >= len(all) {
		return all, nil
	}
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	return all[:count], nil
}

// HStrLen returns the length of a hash field value.
func (s *Store) HStrLen(key, field string) (int64, error) {
	value, ok, err := s.HGet(key, field)
	if err != nil || !ok {
		return 0, err
	}
	return int64(len(value)), nil
}

// HRandField returns random hash fields.
func (s *Store) HRandField(key string, count int) ([]FieldValue, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	hv, ok := e.value.(*hashValue)
	if !ok {
		return nil, ErrWrongType
	}
	fields := sortedKeys(hv.fields)
	if count == 0 || len(fields) == 0 {
		return nil, nil
	}

	if count < 0 {
		n := -count
		if n < 0 {
			n = len(fields)
		}
		out := make([]FieldValue, 0, n)
		for i := 0; i < n; i++ {
			field := fields[rand.IntN(len(fields))]
			out = append(out, FieldValue{Field: field, Value: clone(hv.fields[field])})
		}
		return out, nil
	}
	if count >= len(fields) {
		return hashPairs(hv.fields, fields), nil
	}
	rand.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	return hashPairs(hv.fields, fields[:count]), nil
}

// LInsert inserts value before or after the first pivot. It returns -1 when the
// pivot is missing, 0 when the key does not exist, and the new length otherwise.
func (s *Store) LInsert(key string, before bool, pivot, value []byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	lv, err := s.listLocked(sh, key, now, false)
	if err != nil {
		return 0, err
	}
	if lv == nil {
		return 0, nil
	}

	items := lv.toSlice()
	index := -1
	for i, item := range items {
		if string(item) == string(pivot) {
			index = i
			break
		}
	}
	if index < 0 {
		return -1, nil
	}
	if before {
		index--
	}
	items = append(items, nil)
	copy(items[index+2:], items[index+1:])
	items[index+1] = clone(value)
	lv.reset(items)
	s.touch(sh.data[key])
	return int64(lv.len()), nil
}

// LPos returns the positions of matching elements. rank is 1-based; negative
// ranks search from the tail. count 0 means all matches. maxlen 0 is unlimited.
func (s *Store) LPos(key string, element []byte, rank, count, maxlen int) ([]int64, error) {
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

	target := string(element)
	items := lv.toSlice()
	var positions []int64

	if rank >= 0 {
		skip := rank
		if skip < 1 {
			skip = 1
		}
		matched := 0
		for i, item := range items {
			if maxlen > 0 && i >= maxlen {
				break
			}
			if string(item) != target {
				continue
			}
			matched++
			if matched < skip {
				continue
			}
			positions = append(positions, int64(i))
			if count > 0 && len(positions) >= count {
				break
			}
		}
	} else {
		skip := -rank
		matched := 0
		examined := 0
		for i := len(items) - 1; i >= 0; i-- {
			examined++
			if maxlen > 0 && examined > maxlen {
				break
			}
			if string(items[i]) != target {
				continue
			}
			matched++
			if matched < skip {
				continue
			}
			positions = append(positions, int64(i))
			if count > 0 && len(positions) >= count {
				break
			}
		}
	}
	return positions, nil
}

// Copy duplicates a key, optionally replacing the destination.
func (s *Store) Copy(source, destination string, replace bool) (bool, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	srcShard := s.shardFor(source)
	e, ok := s.lookupRead(srcShard, source, now)
	if !ok {
		return false, nil
	}
	destShard := s.shardFor(destination)
	if _, exists := s.lookupRead(destShard, destination, now); exists && !replace {
		return false, nil
	}

	destShard.data[destination] = s.newEntry(cloneValue(e.value), e.expireAt)
	return true, nil
}

func cloneValue(v value) value {
	switch current := v.(type) {
	case *stringValue:
		return &stringValue{data: clone(current.data)}
	case *hashValue:
		fields := make(map[string][]byte, len(current.fields))
		for field, value := range current.fields {
			fields[field] = clone(value)
		}
		return &hashValue{fields: fields}
	case *listValue:
		items := current.toSlice()
		copied := make([][]byte, len(items))
		for i, item := range items {
			copied[i] = clone(item)
		}
		list := newListValue()
		for _, item := range copied {
			list.pushBack(item)
		}
		return list
	case *setValue:
		members := make(map[string]struct{}, len(current.members))
		for member := range current.members {
			members[member] = struct{}{}
		}
		return &setValue{members: members}
	case *zsetValue:
		zv := newZSetValue()
		for _, entry := range current.list.all() {
			zv.dict[entry.Member] = entry.Score
			zv.list.insert(entry.Member, entry.Score)
		}
		return zv
	}
	return v
}

// ObjectEncoding reports a Redis-like encoding name for a key.
func (s *Store) ObjectEncoding(key string) (string, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return "", ErrNoSuchKey
	}

	switch v := e.value.(type) {
	case *stringValue:
		if _, err := strconv.ParseInt(string(v.data), 10, 64); err == nil && len(v.data) <= 20 {
			return "int", nil
		}
		return "raw", nil
	case *hashValue:
		if len(v.fields) <= 128 {
			return "listpack", nil
		}
		return "hashtable", nil
	case *listValue:
		return "quicklist", nil
	case *setValue:
		if len(v.members) <= 512 && allIntegers(v.members) {
			return "intset", nil
		}
		if len(v.members) <= 128 {
			return "listpack", nil
		}
		return "hashtable", nil
	case *zsetValue:
		if len(v.dict) <= 128 {
			return "listpack", nil
		}
		return "skiplist", nil
	}
	return "raw", nil
}

func allIntegers(members map[string]struct{}) bool {
	for member := range members {
		if _, err := strconv.ParseInt(member, 10, 64); err != nil {
			return false
		}
	}
	return true
}

// MemoryUsage estimates the bytes used by a key.
func (s *Store) MemoryUsage(key string) (int64, bool, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, false, nil
	}

	const overhead = 64
	usage := int64(overhead) + int64(len(key))
	switch v := e.value.(type) {
	case *stringValue:
		usage += int64(len(v.data))
	case *hashValue:
		for field, value := range v.fields {
			usage += int64(len(field)+len(value)) + 16
		}
	case *listValue:
		for _, item := range v.toSlice() {
			usage += int64(len(item)) + 16
		}
	case *setValue:
		for member := range v.members {
			usage += int64(len(member)) + 16
		}
	case *zsetValue:
		for member := range v.dict {
			usage += int64(len(member)) + 24
		}
	}
	return usage, true, nil
}
