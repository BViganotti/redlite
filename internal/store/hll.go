package store

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
)

// HyperLogLog values are stored as opaque strings, exactly like Redis. The
// bytes use Redis' HYLL wire format, so a value produced here can be moved to
// a Redis server (and vice versa) and understood by both implementations.
const (
	hllMagic = "HYLL"

	// HLL_P is the number of hash bits used to address a register. HLL_Q is
	// the number of remaining hash bits used for the rank.
	hllP               = 14
	hllQ               = 64 - hllP
	hllRegisters       = 1 << hllP
	hllPMask           = hllRegisters - 1
	hllBits            = 6
	hllRegisterMax     = (1 << hllBits) - 1
	hllHeaderSize      = 16
	hllDenseSize       = hllHeaderSize + (hllRegisters*hllBits+7)/8
	hllDense           = 0
	hllSparse          = 1
	hllAlphaInf        = 0.721347520444481703680
	hllMurmurSeed      = 0xadc83b19
	hllCacheInvalidBit = 1 << 7
)

// ErrNotHLL is returned when a key holds a string that is not a valid Redis
// HyperLogLog value.
var ErrNotHLL = errors.New("WRONGTYPE Key is not a valid HyperLogLog string value.")

// hllEncoding validates the header and returns the representation. Dense
// values must have exactly the Redis dense length; sparse values are validated
// while decoding.
func hllEncoding(data []byte) (byte, bool) {
	if len(data) < hllHeaderSize || string(data[:4]) != hllMagic {
		return 0, false
	}
	encoding := data[4]
	if encoding > hllSparse {
		return 0, false
	}
	if encoding == hllDense && len(data) != hllDenseSize {
		return 0, false
	}
	return encoding, true
}

func hllCacheValid(data []byte) bool {
	return data[15]&hllCacheInvalidBit == 0
}

func hllCachedCard(data []byte) uint64 {
	return binary.LittleEndian.Uint64(data[8:16])
}

func hllSetCard(data []byte, card uint64) {
	binary.LittleEndian.PutUint64(data[8:16], card)
}

func hllInvalidateCard(data []byte) {
	data[15] |= hllCacheInvalidBit
}

// newDenseHLL returns an empty dense HLL with an invalidated cardinality cache,
// which is the state Redis leaves a newly created HLL in after PFADD/PFMERGE.
func newDenseHLL() []byte {
	data := make([]byte, hllDenseSize)
	copy(data, hllMagic)
	data[4] = hllDense
	hllInvalidateCard(data)
	return data
}

// denseGet reads one 6-bit register. The packing is the one Redis uses in
// hyperloglog.c: registers are stored from the least significant bit upward and
// spill into the following byte when necessary.
func denseGet(registers []byte, index int) byte {
	bit := uint(index * hllBits)
	byteIndex := bit >> 3
	shift := bit & 7
	value := uint16(registers[byteIndex]) >> shift
	if int(byteIndex)+1 < len(registers) {
		value |= uint16(registers[byteIndex+1]) << (8 - shift)
	}
	return byte(value & hllRegisterMax)
}

func denseSet(registers []byte, index int, value byte) {
	bit := uint(index * hllBits)
	byteIndex := bit >> 3
	shift := bit & 7
	v := uint16(value)
	registers[byteIndex] &^= byte(uint16(hllRegisterMax) << shift)
	registers[byteIndex] |= byte(v << shift)
	if int(byteIndex)+1 < len(registers) {
		registers[byteIndex+1] &^= byte(uint16(hllRegisterMax) >> (8 - shift))
		registers[byteIndex+1] |= byte(v >> (8 - shift))
	}
}

// decodeSparseRegisters expands ZERO, XZERO and VAL opcodes into a dense
// register array. It mirrors hllSparseToDense() and rejects truncated or
// overlong representations.
func decodeSparseRegisters(body []byte) ([]byte, error) {
	registers := make([]byte, hllRegisters)
	index := 0
	for p := 0; p < len(body); {
		opcode := body[p]
		switch opcode & 0xc0 {
		case 0x00: // ZERO: 00xxxxxx, 1..64 zero registers.
			run := int(opcode&0x3f) + 1
			if index+run > hllRegisters {
				return nil, ErrNotHLL
			}
			index += run
			p++
		case 0x40: // XZERO: 01xxxxxx yyyyyyyy, 1..16384 zero registers.
			if p+1 >= len(body) {
				return nil, ErrNotHLL
			}
			run := (int(opcode&0x3f)<<8 | int(body[p+1])) + 1
			if index+run > hllRegisters {
				return nil, ErrNotHLL
			}
			index += run
			p += 2
		default: // VAL: 1vvvvvxx, 1..32 in a run of 1..4 registers.
			run := int(opcode&0x03) + 1
			value := (opcode>>2)&0x1f + 1
			if index+run > hllRegisters {
				return nil, ErrNotHLL
			}
			for i := 0; i < run; i++ {
				registers[index+i] = value
			}
			index += run
			p++
		}
	}
	if index != hllRegisters {
		return nil, ErrNotHLL
	}
	return registers, nil
}

// decodeHLLRegisters validates a value and expands it to 16384 dense registers.
func decodeHLLRegisters(data []byte) ([]byte, error) {
	encoding, ok := hllEncoding(data)
	if !ok {
		return nil, ErrNotHLL
	}
	if encoding == hllSparse {
		return decodeSparseRegisters(data[hllHeaderSize:])
	}
	source := data[hllHeaderSize:]
	registers := make([]byte, hllRegisters)
	for i := range registers {
		registers[i] = denseGet(source, i)
	}
	return registers, nil
}

// encodeDenseHLL packs registers into a Redis dense HLL. The cardinality cache
// is invalidated because this is only used for values that were just modified.
func encodeDenseHLL(registers []byte) []byte {
	data := newDenseHLL()
	dense := data[hllHeaderSize:]
	for i, value := range registers {
		if value != 0 {
			denseSet(dense, i, value)
		}
	}
	return data
}

// murmurHash64A is the endian-neutral 64-bit MurmurHash2 variant Redis uses.
func murmurHash64A(data []byte, seed uint64) uint64 {
	const m = 0xc6a4a7935bd1e995
	const r = 47

	h := seed ^ (uint64(len(data)) * m)
	full := len(data) &^ 7
	for i := 0; i < full; i += 8 {
		k := binary.LittleEndian.Uint64(data[i:])
		k *= m
		k ^= k >> r
		k *= m
		h ^= k
		h *= m
	}

	switch len(data) & 7 {
	case 7:
		h ^= uint64(data[full+6]) << 48
		fallthrough
	case 6:
		h ^= uint64(data[full+5]) << 40
		fallthrough
	case 5:
		h ^= uint64(data[full+4]) << 32
		fallthrough
	case 4:
		h ^= uint64(data[full+3]) << 24
		fallthrough
	case 3:
		h ^= uint64(data[full+2]) << 16
		fallthrough
	case 2:
		h ^= uint64(data[full+1]) << 8
		fallthrough
	case 1:
		h ^= uint64(data[full])
		h *= m
	}

	h ^= h >> r
	h *= m
	h ^= h >> r
	return h
}

// hllPatLen hashes an element and returns the register index and the rank
// (the 1-based length of the trailing-zero pattern plus one).
func hllPatLen(element []byte) (index int, rank byte) {
	hash := murmurHash64A(element, hllMurmurSeed)
	index = int(hash & hllPMask)
	hash >>= hllP
	hash |= uint64(1) << hllQ
	return index, byte(bits.TrailingZeros64(hash) + 1)
}

func denseAdd(registers []byte, element []byte) bool {
	index, rank := hllPatLen(element)
	if rank <= denseGet(registers, index) {
		return false
	}
	denseSet(registers, index, rank)
	return true
}

// hllSigma is Ertl's sigma helper from Redis' hyperloglog.c.
func hllSigma(x float64) float64 {
	if x == 1 {
		return math.Inf(1)
	}
	y := 1.0
	z := x
	for {
		x *= x
		zPrime := z
		z += x * y
		y += y
		if zPrime == z {
			return z
		}
	}
}

// hllTau is Ertl's tau helper from Redis' hyperloglog.c.
func hllTau(x float64) float64 {
	if x == 0 || x == 1 {
		return 0
	}
	y := 1.0
	z := 1 - x
	for {
		x = math.Sqrt(x)
		zPrime := z
		y *= 0.5
		d := 1 - x
		z -= d * d * y
		if zPrime == z {
			return z / 3
		}
	}
}

// hllEstimate applies the same estimator and rounding as Redis' hllCount().
func hllEstimate(registers []byte) uint64 {
	const m = float64(hllRegisters)
	var histogram [64]int
	for _, value := range registers {
		histogram[value]++
	}

	z := m * hllTau((m-float64(histogram[hllQ+1]))/m)
	for j := hllQ; j >= 1; j-- {
		z += float64(histogram[j])
		z *= 0.5
	}
	z += m * hllSigma(float64(histogram[0])/m)

	estimate := hllAlphaInf * m * m / z
	return uint64(estimate + 0.5)
}

// PFAdd adds elements and reports whether the cardinality changed.
func (s *Store) PFAdd(key string, elements [][]byte) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	e, exists := s.lookupWrite(sh, key, now)
	if !exists {
		data := newDenseHLL()
		registers := data[hllHeaderSize:]
		for _, element := range elements {
			denseAdd(registers, element)
		}
		sh.data[key] = s.newEntry(&stringValue{data: data}, 0)
		s.sets.Add(1)
		// Redis creates the key on PFADD and reports the change even when no
		// elements were supplied.
		return 1, nil
	}

	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}
	encoding, ok := hllEncoding(sv.data)
	if !ok {
		return 0, ErrNotHLL
	}

	if encoding == hllDense {
		registers := sv.data[hllHeaderSize:]
		changed := false
		for _, element := range elements {
			if denseAdd(registers, element) {
				changed = true
			}
		}
		if !changed {
			return 0, nil
		}
		hllInvalidateCard(sv.data)
		s.touch(e)
		return 1, nil
	}

	registers, err := decodeSparseRegisters(sv.data[hllHeaderSize:])
	if err != nil {
		return 0, err
	}
	changed := false
	for _, element := range elements {
		if denseAdd(registers, element) {
			changed = true
		}
	}
	if !changed {
		return 0, nil
	}
	sv.data = encodeDenseHLL(registers)
	s.touch(e)
	return 1, nil
}

// PFCount returns the approximate number of distinct elements across keys.
func (s *Store) PFCount(keys []string) (int64, error) {
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	if len(keys) == 1 {
		key := keys[0]
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			return 0, nil
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrWrongType
		}
		if _, ok := hllEncoding(sv.data); !ok {
			return 0, ErrNotHLL
		}
		if hllCacheValid(sv.data) {
			return int64(hllCachedCard(sv.data)), nil
		}
		registers, err := decodeHLLRegisters(sv.data)
		if err != nil {
			return 0, err
		}
		card := hllEstimate(registers)
		hllSetCard(sv.data, card)
		return int64(card), nil
	}

	union := make([]byte, hllRegisters)
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			continue
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return 0, ErrWrongType
		}
		registers, err := decodeHLLRegisters(sv.data)
		if err != nil {
			return 0, err
		}
		for i, value := range registers {
			if value > union[i] {
				union[i] = value
			}
		}
	}
	return int64(hllEstimate(union)), nil
}

// PFMerge unions the destination's existing registers and all sources into the
// destination, storing a valid dense HLL.
func (s *Store) PFMerge(destination string, sources []string) error {
	keys := make([]string, 0, len(sources)+1)
	keys = append(keys, destination)
	keys = append(keys, sources...)
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	union := make([]byte, hllRegisters)
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			continue
		}
		sv, ok := e.value.(*stringValue)
		if !ok {
			return ErrWrongType
		}
		registers, err := decodeHLLRegisters(sv.data)
		if err != nil {
			return err
		}
		for i, value := range registers {
			if value > union[i] {
				union[i] = value
			}
		}
	}

	sh := s.shardFor(destination)
	data := encodeDenseHLL(union)
	if e, exists := s.lookupWrite(sh, destination, now); exists {
		sv, ok := e.value.(*stringValue)
		if !ok {
			return ErrWrongType
		}
		sv.data = data
		s.touch(e)
		return nil
	}
	sh.data[destination] = s.newEntry(&stringValue{data: data}, 0)
	s.sets.Add(1)
	return nil
}
