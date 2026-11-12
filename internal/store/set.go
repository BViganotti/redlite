package store

import (
	"math/rand/v2"
	"sort"
)

// setLocked returns the set at key, creating it when create is true.
func (s *Store) setLocked(sh *shard, key string, now int64, create bool) (*setValue, error) {
	e, ok := s.lookupWrite(sh, key, now)
	if !ok {
		if !create {
			return nil, nil
		}
		sv := &setValue{members: make(map[string]struct{})}
		sh.data[key] = s.newEntry(sv, 0)
		s.sets.Add(1)
		return sv, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return nil, ErrWrongType
	}
	return sv, nil
}

// SAdd adds members and reports how many were new.
func (s *Store) SAdd(key string, members [][]byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	sv, err := s.setLocked(sh, key, now, true)
	if err != nil {
		return 0, err
	}
	var added int64
	for _, member := range members {
		if _, ok := sv.members[string(member)]; !ok {
			added++
		}
		sv.members[string(member)] = struct{}{}
	}
	if added > 0 {
		s.touch(sh.data[key])
	}
	return added, nil
}

// SRem removes members and reports how many existed.
func (s *Store) SRem(key string, members [][]byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	sv, err := s.setLocked(sh, key, now, false)
	if err != nil {
		return 0, err
	}
	if sv == nil {
		return 0, nil
	}
	var removed int64
	for _, member := range members {
		if _, ok := sv.members[string(member)]; ok {
			delete(sv.members, string(member))
			removed++
		}
	}
	if len(sv.members) == 0 {
		delete(sh.data, key)
	} else if removed > 0 {
		s.touch(sh.data[key])
	}
	return removed, nil
}

// SCard returns the cardinality.
func (s *Store) SCard(key string) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return 0, ErrWrongType
	}
	return int64(len(sv.members)), nil
}

// SIsMember reports membership.
func (s *Store) SIsMember(key string, member []byte) (bool, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return false, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return false, ErrWrongType
	}
	_, exists := sv.members[string(member)]
	return exists, nil
}

// SMembers returns every member, sorted so replies are deterministic.
func (s *Store) SMembers(key string) ([][]byte, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return nil, ErrWrongType
	}
	return setMembers(sv.members), nil
}

// SInter returns the intersection of the sets at keys.
func (s *Store) SInter(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, intersect)
}

// SUnion returns the union of the sets at keys.
func (s *Store) SUnion(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, union)
}

// SDiff returns the members of the first set not present in the others.
func (s *Store) SDiff(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, difference)
}

func (s *Store) setAlgebra(keys []string, op func([]map[string]struct{}) map[string]struct{}) ([][]byte, error) {
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	sets := make([]map[string]struct{}, 0, len(keys))
	for _, key := range keys {
		sh := s.shardFor(key)
		e, ok := s.lookupRead(sh, key, now)
		if !ok {
			sets = append(sets, nil)
			continue
		}
		sv, ok := e.value.(*setValue)
		if !ok {
			return nil, ErrWrongType
		}
		sets = append(sets, sv.members)
	}
	return setMembers(op(sets)), nil
}

func intersect(sets []map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	if len(sets) == 0 || sets[0] == nil {
		return out
	}
	for member := range sets[0] {
		present := true
		for _, other := range sets[1:] {
			if other == nil {
				return out
			}
			if _, ok := other[member]; !ok {
				present = false
				break
			}
		}
		if present {
			out[member] = struct{}{}
		}
	}
	return out
}

func union(sets []map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for _, set := range sets {
		for member := range set {
			out[member] = struct{}{}
		}
	}
	return out
}

func difference(sets []map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	if len(sets) == 0 || sets[0] == nil {
		return out
	}
	for member := range sets[0] {
		keep := true
		for _, other := range sets[1:] {
			if _, ok := other[member]; ok {
				keep = false
				break
			}
		}
		if keep {
			out[member] = struct{}{}
		}
	}
	return out
}

// SPop removes and returns up to count random members.
func (s *Store) SPop(key string, count int) ([][]byte, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	sv, err := s.setLocked(sh, key, now, false)
	if err != nil {
		return nil, err
	}
	if sv == nil || count <= 0 {
		return nil, nil
	}

	members := sortedSetKeys(sv.members)
	if count < len(members) {
		rand.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
		members = members[:count]
	}
	out := make([][]byte, 0, len(members))
	for _, member := range members {
		delete(sv.members, member)
		out = append(out, []byte(member))
	}
	if len(sv.members) == 0 {
		delete(sh.data, key)
	} else if len(out) > 0 {
		s.touch(sh.data[key])
	}
	return out, nil
}

// SRandMember returns random members without removing them. A negative count
// allows repeated members.
func (s *Store) SRandMember(key string, count int) ([][]byte, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return nil, ErrWrongType
	}
	if count == 0 || len(sv.members) == 0 {
		return nil, nil
	}

	all := sortedSetKeys(sv.members)
	if count < 0 {
		n := -count
		if n < 0 {
			n = len(all)
		}
		out := make([][]byte, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, []byte(all[rand.IntN(len(all))]))
		}
		return out, nil
	}

	if count >= len(all) {
		return setMembers(sv.members), nil
	}
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	out := make([][]byte, 0, count)
	for _, member := range all[:count] {
		out = append(out, []byte(member))
	}
	return out, nil
}

// SScan walks a set from cursor, returning the next cursor and matching
// members.
func (s *Store) SScan(key string, cursor uint64, pattern string, count int) (uint64, []string, error) {
	if count <= 0 {
		count = 10
	}
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil, nil
	}
	sv, ok := e.value.(*setValue)
	if !ok {
		return 0, nil, ErrWrongType
	}
	members := sortedSetKeys(sv.members)
	if cursor >= uint64(len(members)) {
		return 0, nil, nil
	}

	var (
		out      []string
		examined int
	)
	for examined < count && cursor < uint64(len(members)) {
		member := members[cursor]
		cursor++
		examined++
		if pattern != "" && pattern != "*" && !Match(pattern, member) {
			continue
		}
		out = append(out, member)
	}
	if cursor >= uint64(len(members)) {
		cursor = 0
	}
	return cursor, out, nil
}

// SMove atomically moves member from source to destination. It reports
// whether the member was moved. The destination is type-checked before the
// source is modified, matching Redis.
func (s *Store) SMove(source, destination string, member []byte) (bool, error) {
	locked := s.lockKeys([]string{source, destination})
	defer s.unlockKeys(locked)

	now := s.now()
	srcShard := s.shardFor(source)
	srcEntry, ok := s.lookupWrite(srcShard, source, now)
	if !ok {
		return false, nil
	}
	src, ok := srcEntry.value.(*setValue)
	if !ok {
		return false, ErrWrongType
	}

	target := string(member)
	if _, exists := src.members[target]; !exists {
		return false, nil
	}

	dstShard := s.shardFor(destination)
	dst, err := s.setLocked(dstShard, destination, now, false)
	if err != nil {
		return false, err
	}
	if dst == nil {
		dst = &setValue{members: make(map[string]struct{})}
		dstShard.data[destination] = s.newEntry(dst, 0)
		s.sets.Add(1)
	}

	delete(src.members, target)
	dst.members[target] = struct{}{}
	s.touch(dstShard.data[destination])
	if len(src.members) == 0 {
		delete(srcShard.data, source)
	} else {
		s.touch(srcShard.data[source])
	}
	return true, nil
}

// SStore computes an intersection, union or difference and stores the result
// at destination, returning its cardinality. An empty result deletes the
// destination, as Redis does.
func (s *Store) SStore(op string, destination string, keys []string) (int64, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	sets := make([]map[string]struct{}, 0, len(keys))
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			sets = append(sets, nil)
			continue
		}
		sv, ok := e.value.(*setValue)
		if !ok {
			return 0, ErrWrongType
		}
		sets = append(sets, sv.members)
	}

	var result map[string]struct{}
	switch op {
	case "INTER":
		result = intersect(sets)
	case "UNION":
		result = union(sets)
	case "DIFF":
		result = difference(sets)
	default:
		return 0, ErrWrongType
	}

	destShard := s.shardFor(destination)
	if len(result) == 0 {
		delete(destShard.data, destination)
		return 0, nil
	}
	members := make(map[string]struct{}, len(result))
	for member := range result {
		members[member] = struct{}{}
	}
	destShard.data[destination] = s.newEntry(&setValue{members: members}, 0)
	s.sets.Add(1)
	return int64(len(members)), nil
}

// SInterCard counts the intersection of the sets at keys, stopping once limit
// is reached. A limit of zero means no limit.
func (s *Store) SInterCard(keys []string, limit int64) (int64, error) {
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	sets := make([]map[string]struct{}, 0, len(keys))
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			// Still type-check the remaining keys before reporting an empty
			// intersection, so a later WRONGTYPE is not hidden by a miss.
			sets = append(sets, nil)
			continue
		}
		sv, ok := e.value.(*setValue)
		if !ok {
			return 0, ErrWrongType
		}
		sets = append(sets, sv.members)
	}
	if len(sets) == 0 {
		return 0, nil
	}
	for _, set := range sets {
		if set == nil {
			return 0, nil
		}
	}
	// Walk the smallest set so a limit short-circuits early.
	smallest := 0
	for i := 1; i < len(sets); i++ {
		if len(sets[i]) < len(sets[smallest]) {
			smallest = i
		}
	}
	var count int64
	for member := range sets[smallest] {
		present := true
		for i, set := range sets {
			if i == smallest {
				continue
			}
			if _, ok := set[member]; !ok {
				present = false
				break
			}
		}
		if present {
			count++
			if limit > 0 && count >= limit {
				return count, nil
			}
		}
	}
	return count, nil
}

func setMembers(m map[string]struct{}) [][]byte {
	members := make([]string, 0, len(m))
	for member := range m {
		members = append(members, member)
	}
	sort.Strings(members)
	out := make([][]byte, 0, len(members))
	for _, member := range members {
		out = append(out, []byte(member))
	}
	return out
}
