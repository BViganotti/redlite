package store

import (
	"encoding/binary"
	"errors"
	"sort"
)

// HyperLogLog values are stored as opaque strings, like Redis. Redlite keeps an
// exact set of 64-bit hashes instead of probabilistic registers, so PFCOUNT is
// exact; the value is not a Redis HLL and cannot be moved to Redis as one.
const hllMagic = "REDLHLL1\n"

// ErrNotHLL is returned when a key holds a string that is not a redlite HLL.
var ErrNotHLL = errors.New("WRONGTYPE Key is not a valid HyperLogLog string value.")

func hash64(data []byte) uint64 {
	var hash uint64 = 14695981039346656037
	for _, b := range data {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return hash
}

func decodeHLL(data []byte) (map[uint64]struct{}, error) {
	if len(data) < len(hllMagic) || string(data[:len(hllMagic)]) != hllMagic {
		return nil, ErrNotHLL
	}
	body := data[len(hllMagic):]
	if len(body)%8 != 0 {
		return nil, ErrNotHLL
	}
	out := make(map[uint64]struct{}, len(body)/8)
	for i := 0; i+8 <= len(body); i += 8 {
		out[binary.LittleEndian.Uint64(body[i:i+8])] = struct{}{}
	}
	return out, nil
}

func encodeHLL(set map[uint64]struct{}) []byte {
	hashes := make([]uint64, 0, len(set))
	for hash := range set {
		hashes = append(hashes, hash)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })

	out := make([]byte, 0, len(hllMagic)+len(hashes)*8)
	out = append(out, hllMagic...)
	var buf [8]byte
	for _, hash := range hashes {
		binary.LittleEndian.PutUint64(buf[:], hash)
		out = append(out, buf[:]...)
	}
	return out
}

// PFAdd adds elements and reports whether the cardinality changed.
func (s *Store) PFAdd(key string, elements [][]byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	e, exists := s.lookupWrite(sh, key, now)
	set := make(map[uint64]struct{})
	var sv *stringValue
	if exists {
		current, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrNotHLL
		}
		sv = current
		parsed, err := decodeHLL(sv.data)
		if err != nil {
			return 0, err
		}
		set = parsed
	}

	changed := false
	for _, element := range elements {
		hash := hash64(element)
		if _, ok := set[hash]; !ok {
			set[hash] = struct{}{}
			changed = true
		}
	}
	if !changed && sv != nil {
		return 0, nil
	}

	encoded := encodeHLL(set)
	if sv == nil {
		sh.data[key] = s.newEntry(&stringValue{data: encoded}, 0)
		s.sets.Add(1)
	} else {
		sv.data = encoded
		s.touch(e)
	}
	if changed {
		return 1, nil
	}
	return 0, nil
}

// PFCount returns the exact number of distinct elements across keys.
func (s *Store) PFCount(keys []string) (int64, error) {
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	union := make(map[uint64]struct{})
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			continue
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrNotHLL
		}
		parsed, err := decodeHLL(sv.data)
		if err != nil {
			return 0, err
		}
		for hash := range parsed {
			union[hash] = struct{}{}
		}
	}
	return int64(len(union)), nil
}

// PFMerge unions sources into destination.
func (s *Store) PFMerge(destination string, sources []string) error {
	keys := append([]string{destination}, sources...)
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	union := make(map[uint64]struct{})
	for _, key := range sources {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			continue
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return ErrNotHLL
		}
		parsed, err := decodeHLL(sv.data)
		if err != nil {
			return err
		}
		for hash := range parsed {
			union[hash] = struct{}{}
		}
	}

	destShard := s.shardFor(destination)
	destShard.data[destination] = s.newEntry(&stringValue{data: encodeHLL(union)}, 0)
	s.sets.Add(1)
	return nil
}
