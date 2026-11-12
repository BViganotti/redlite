package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"time"
)

// This file implements Redis' RDB format for the five value types we support.
// The writer emits the plain (non-listpack) encodings, which Redis reads. The
// reader accepts both the plain encodings and the compact encodings Redis 7
// writes by default: intset, listpack, and quicklist-of-listpacks.

const rdbVersion = 10

// RDB errors.
var (
	ErrRDBFormat      = errors.New("invalid RDB file")
	ErrRDBUnsupported = errors.New("unsupported RDB encoding")
)

// dumpVersion is the RDB object version embedded in DUMP payloads. Version 9
// is readable by every Redis release from 5 onward.
const dumpVersion = 9

// Errors returned by RESTORE.
var (
	ErrDumpPayload = errors.New("DUMP payload version or checksum are wrong")
	ErrBusyKey     = errors.New("BUSYKEY Target key name already exists.")
)

// Dump encodes one key in the Redis DUMP payload format.
func (s *Store) Dump(key string) ([]byte, bool, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, false, nil
	}

	writer := &rdbWriter{w: io.Discard}
	entry := snapshotEntry{key: key, kind: e.value.kind(), expireAt: e.expireAt}
	switch v := e.value.(type) {
	case *stringValue:
		entry.str = v.data
	case *hashValue:
		entry.hash = v.fields
	case *listValue:
		entry.list = v.toSlice()
	case *setValue:
		entry.set = sortedSetKeys(v.members)
	case *zsetValue:
		entry.zset = v.list.all()
	}
	writeDumpObject(writer, entry)
	if writer.err != nil {
		return nil, false, writer.err
	}

	payload := append([]byte(nil), writer.buf...)
	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], dumpVersion)
	payload = append(payload, version[:]...)
	sum := crc64(payload)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], sum)
	payload = append(payload, checksum[:]...)
	return payload, true, nil
}

func writeDumpObject(w *rdbWriter, entry snapshotEntry) {
	switch entry.kind {
	case TypeString:
		w.byte(0)
		w.string(entry.str)
	case TypeList:
		w.byte(1)
		w.length(len(entry.list))
		for _, item := range entry.list {
			w.string(item)
		}
	case TypeSet:
		w.byte(2)
		w.length(len(entry.set))
		for _, member := range entry.set {
			w.string([]byte(member))
		}
	case TypeZSet:
		w.byte(5)
		w.length(len(entry.zset))
		for _, member := range entry.zset {
			w.string([]byte(member.Member))
			w.double(member.Score)
		}
	case TypeHash:
		w.byte(4)
		w.length(len(entry.hash))
		for _, field := range sortedKeys(entry.hash) {
			w.string([]byte(field))
			w.string(entry.hash[field])
		}
	}
}

// Restore decodes a DUMP payload and installs it at key. expireAt is an
// absolute unix-millisecond deadline, or zero for no expiry.
func (s *Store) Restore(key string, payload []byte, expireAt int64, replace bool) error {
	if len(payload) < 11 {
		return ErrDumpPayload
	}
	body := payload[:len(payload)-10]
	version := binary.LittleEndian.Uint16(payload[len(payload)-10 : len(payload)-8])
	if version > 20 {
		return ErrDumpPayload
	}
	want := binary.LittleEndian.Uint64(payload[len(payload)-8:])
	if want != 0 && crc64(payload[:len(payload)-8]) != want {
		return ErrDumpPayload
	}
	if len(body) < 1 {
		return ErrDumpPayload
	}

	reader := &rdbReader{r: bytes.NewReader(body[1:])}
	parsed, err := reader.readObject(body[0])
	if err != nil {
		return ErrDumpPayload
	}

	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)
	if _, exists := s.lookupWrite(sh, key, now); exists && !replace {
		return ErrBusyKey
	}
	if expireAt != 0 && expireAt <= now {
		delete(sh.data, key)
		return nil
	}
	sh.data[key] = s.newEntry(parsed, expireAt)
	s.sets.Add(1)
	return nil
}

// CRC64 (Jones polynomial) as used by Redis.
var (
	crc64Table [256]uint64
	crc64Once  sync.Once
)

func initCRC64() {
	// Reflected form of the Jones polynomial, which is what Redis' crc64
	// implementation uses.
	const polynomial = 0x95ac9329ac4bc9b5
	for i := 0; i < 256; i++ {
		crc := uint64(i)
		for bit := 0; bit < 8; bit++ {
			if crc&1 == 1 {
				crc = (crc >> 1) ^ polynomial
			} else {
				crc >>= 1
			}
		}
		crc64Table[i] = crc
	}
}

func crc64(data []byte) uint64 {
	crc64Once.Do(initCRC64)
	var crc uint64
	for _, b := range data {
		crc = crc64Table[byte(crc)^b] ^ (crc >> 8)
	}
	return crc
}

// SaveRDB writes the keyspace in Redis RDB format.
func (s *Store) SaveRDB(w io.Writer) error {
	entries := s.snapshotEntries()

	writer := &rdbWriter{w: w}
	writer.raw([]byte("REDIS0010"))
	writer.aux("redis-ver", "7.0.0")
	writer.aux("redis-bits", "64")
	writer.aux("ctime", strconv.FormatInt(time.Now().Unix(), 10))
	writer.aux("used-mem", "0")
	writer.byte(0xFE) // SELECTDB
	writer.length(0)

	for _, e := range entries {
		if e.expireAt != 0 {
			writer.byte(0xFC) // EXPIRETIME_MS
			writer.uint64LE(uint64(e.expireAt))
		}
		switch e.kind {
		case TypeString:
			writer.byte(0) // RDB_TYPE_STRING
			writer.string([]byte(e.key))
			writer.string(e.str)
		case TypeList:
			writer.byte(1) // RDB_TYPE_LIST
			writer.string([]byte(e.key))
			writer.length(len(e.list))
			for _, item := range e.list {
				writer.string(item)
			}
		case TypeSet:
			writer.byte(2) // RDB_TYPE_SET
			writer.string([]byte(e.key))
			writer.length(len(e.set))
			for _, member := range e.set {
				writer.string([]byte(member))
			}
		case TypeZSet:
			writer.byte(5) // RDB_TYPE_ZSET_2 (binary doubles)
			writer.string([]byte(e.key))
			writer.length(len(e.zset))
			for _, member := range e.zset {
				writer.string([]byte(member.Member))
				writer.double(member.Score)
			}
		case TypeHash:
			writer.byte(4) // RDB_TYPE_HASH
			writer.string([]byte(e.key))
			writer.length(len(e.hash))
			for _, field := range sortedKeys(e.hash) {
				writer.string([]byte(field))
				writer.string(e.hash[field])
			}
		}
		if writer.err != nil {
			return writer.err
		}
	}

	writer.byte(0xFF) // EOF
	if writer.err != nil {
		return writer.err
	}

	checksum := crc64(writer.buf)
	writer.buf = nil
	if _, err := w.Write(checksumBytes(checksum)); err != nil {
		return err
	}
	return writer.err
}

type rdbWriter struct {
	w   io.Writer
	buf []byte // accumulates bytes for the checksum
	err error
}

func (w *rdbWriter) raw(data []byte) {
	if w.err != nil {
		return
	}
	w.buf = append(w.buf, data...)
	if _, err := w.w.Write(data); err != nil {
		w.err = err
	}
}

func (w *rdbWriter) byte(b byte) {
	w.raw([]byte{b})
}

func (w *rdbWriter) length(n int) {
	switch {
	case n < 1<<6:
		w.byte(byte(n))
	case n < 1<<14:
		w.byte(byte(n>>8) | 0x40)
		w.byte(byte(n))
	default:
		w.byte(0x80)
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], uint32(n))
		w.raw(buf[:])
	}
}

func (w *rdbWriter) string(data []byte) {
	w.length(len(data))
	w.raw(data)
}

func (w *rdbWriter) uint64LE(v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	w.raw(buf[:])
}

func (w *rdbWriter) double(v float64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
	w.raw(buf[:])
}

func (w *rdbWriter) aux(key, value string) {
	w.byte(0xFA)
	w.string([]byte(key))
	w.string([]byte(value))
}

func checksumBytes(sum uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], sum)
	return buf[:]
}

// LoadRDB replaces the keyspace with an RDB payload.
func (s *Store) LoadRDB(r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxSnapshotSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxSnapshotSize {
		return ErrSnapshotTooBig
	}
	if len(data) < 9+8 || string(data[:5]) != "REDIS" {
		return ErrRDBFormat
	}

	version, err := strconv.Atoi(string(data[5:9]))
	if err != nil || version < 1 || version > 20 {
		return fmt.Errorf("%w: version %q", ErrRDBFormat, data[5:9])
	}

	// The trailing checksum covers everything before it. A zero checksum means
	// the writer disabled verification.
	body := data[:len(data)-8]
	want := binary.LittleEndian.Uint64(data[len(data)-8:])
	if want != 0 && crc64(body) != want {
		return fmt.Errorf("%w: checksum mismatch", ErrRDBFormat)
	}

	reader := &rdbReader{r: bytes.NewReader(data[9 : len(data)-8])}
	now := s.now()
	parsed := make(map[string]*entry)
	var expireAt int64

	for {
		opcode, err := reader.readByte()
		if err != nil {
			return ErrRDBFormat
		}
		switch opcode {
		case 0xFF: // EOF
			return s.installRDB(parsed)
		case 0xFA: // AUX
			if _, err := reader.string(); err != nil {
				return err
			}
			if _, err := reader.string(); err != nil {
				return err
			}
		case 0xFB: // RESIZEDB
			if _, _, err := reader.length(); err != nil {
				return err
			}
			if _, _, err := reader.length(); err != nil {
				return err
			}
		case 0xFE: // SELECTDB
			if _, _, err := reader.length(); err != nil {
				return err
			}
		case 0xFC: // EXPIRETIME_MS
			raw, err := reader.uint64LE()
			if err != nil {
				return err
			}
			expireAt = int64(raw)
		case 0xFD: // EXPIRETIME (seconds)
			raw, err := reader.uint32LE()
			if err != nil {
				return err
			}
			expireAt = int64(raw) * 1000
		default:
			offset := int64(len(data[9:len(data)-8])) - int64(reader.r.Len())
			key, value, err := reader.object(opcode)
			if err != nil {
				return fmt.Errorf("type %d at offset %d: %w", opcode, offset, err)
			}
			if expireAt != 0 && expireAt <= now {
				expireAt = 0
				continue
			}
			parsed[key] = &entry{value: value, expireAt: expireAt}
			expireAt = 0
		}
	}
}

func (s *Store) installRDB(parsed map[string]*entry) error {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)
	for _, sh := range s.shards {
		sh.data = make(map[string]*entry)
	}
	for key, entry := range parsed {
		entry.version = s.nextVersion.Add(1)
		s.shardFor(key).data[key] = entry
	}
	s.epoch.Add(1)
	return nil
}

type rdbReader struct {
	r *bytes.Reader
}

func (r *rdbReader) readByte() (byte, error) {
	b, err := r.r.ReadByte()
	if err != nil {
		return 0, ErrRDBFormat
	}
	return b, nil
}

func (r *rdbReader) readFull(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return nil, ErrRDBFormat
	}
	return buf, nil
}

func (r *rdbReader) uint32LE() (uint32, error) {
	buf, err := r.readFull(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf), nil
}

func (r *rdbReader) uint64LE() (uint64, error) {
	buf, err := r.readFull(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf), nil
}

func (r *rdbReader) uint16LE() (uint16, error) {
	buf, err := r.readFull(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf), nil
}

// length decodes a length prefix. encoded is true for the special string
// encodings (int8/16/32 and LZF), whose payload must be read by string().
func (r *rdbReader) length() (value int, encoded bool, err error) {
	first, err := r.readByte()
	if err != nil {
		return 0, false, err
	}
	switch first >> 6 {
	case 0: // 6-bit
		return int(first & 0x3F), false, nil
	case 1: // 14-bit
		second, err := r.readByte()
		if err != nil {
			return 0, false, err
		}
		return int(first&0x3F)<<8 | int(second), false, nil
	case 2:
		if first == 0x80 {
			raw, err := r.uint32LE()
			if err != nil {
				return 0, false, err
			}
			return int(raw), false, nil
		}
		if first == 0x81 {
			raw, err := r.uint64LE()
			if err != nil {
				return 0, false, err
			}
			return int(raw), false, nil
		}
		return 0, false, ErrRDBFormat
	default: // 11xxxxxx: special string encoding
		return int(first & 0x3F), true, nil
	}
}

func (r *rdbReader) string() ([]byte, error) {
	size, encoded, err := r.length()
	if err != nil {
		return nil, err
	}
	if !encoded {
		if size > r.r.Len() {
			return nil, ErrRDBFormat
		}
		return r.readFull(size)
	}

	switch size {
	case 0: // int8
		b, err := r.readByte()
		return []byte(strconv.FormatInt(int64(int8(b)), 10)), err
	case 1: // int16
		raw, err := r.uint16LE()
		return []byte(strconv.FormatInt(int64(int16(raw)), 10)), err
	case 2: // int32
		raw, err := r.uint32LE()
		return []byte(strconv.FormatInt(int64(int32(raw)), 10)), err
	case 3: // LZF
		compressedLen, _, err := r.length()
		if err != nil {
			return nil, err
		}
		plainLen, _, err := r.length()
		if err != nil {
			return nil, err
		}
		input, err := r.readFull(compressedLen)
		if err != nil {
			return nil, err
		}
		return lzfDecompress(input, plainLen)
	default:
		return nil, fmt.Errorf("%w: string encoding %d", ErrRDBUnsupported, size)
	}
}

func (r *rdbReader) object(kind byte) (string, value, error) {
	keyBytes, err := r.string()
	if err != nil {
		return "", nil, err
	}
	v, err := r.readObject(kind)
	if err != nil {
		return "", nil, err
	}
	return string(keyBytes), v, nil
}

// checkCount rejects a collection length that cannot possibly fit in the
// remaining payload, preventing a hostile DUMP/RESTORE from allocating
// gigabytes for an empty body.
func (r *rdbReader) checkCount(count int) error {
	if count < 0 || count > r.r.Len() {
		return ErrRDBFormat
	}
	return nil
}

// readObject decodes an object body without its key.
func (r *rdbReader) readObject(kind byte) (value, error) {
	switch kind {
	case 0: // string
		data, err := r.string()
		if err != nil {
			return nil, err
		}
		return &stringValue{data: data}, nil

	case 1: // list
		count, _, err := r.length()
		if err != nil {
			return nil, err
		}
		if err := r.checkCount(count); err != nil {
			return nil, err
		}
		list := newListValue()
		for i := 0; i < count; i++ {
			item, err := r.string()
			if err != nil {
				return nil, err
			}
			list.pushBack(item)
		}
		return list, nil

	case 2: // set
		count, _, err := r.length()
		if err != nil {
			return nil, err
		}
		if err := r.checkCount(count); err != nil {
			return nil, err
		}
		members := make(map[string]struct{}, count)
		for i := 0; i < count; i++ {
			member, err := r.string()
			if err != nil {
				return nil, err
			}
			members[string(member)] = struct{}{}
		}
		return &setValue{members: members}, nil

	case 3, 5: // zset (string score / binary double)
		count, _, err := r.length()
		if err != nil {
			return nil, err
		}
		if err := r.checkCount(count); err != nil {
			return nil, err
		}
		zv := newZSetValue()
		for i := 0; i < count; i++ {
			member, err := r.string()
			if err != nil {
				return nil, err
			}
			var score float64
			if kind == 3 {
				score, err = r.legacyDouble()
			} else {
				var raw uint64
				raw, err = r.uint64LE()
				score = math.Float64frombits(raw)
			}
			if err != nil {
				return nil, err
			}
			zv.dict[string(member)] = score
			zv.list.insert(string(member), score)
		}
		return zv, nil

	case 4: // hash
		count, _, err := r.length()
		if err != nil {
			return nil, err
		}
		if err := r.checkCount(count); err != nil {
			return nil, err
		}
		fields := make(map[string][]byte, count)
		for i := 0; i < count; i++ {
			field, err := r.string()
			if err != nil {
				return nil, err
			}
			value, err := r.string()
			if err != nil {
				return nil, err
			}
			fields[string(field)] = value
		}
		return &hashValue{fields: fields}, nil

	case 11: // intset
		return r.intset()

	case 18: // quicklist v2 (listpack nodes)
		return r.quicklist()

	case 16: // hash listpack
		return r.listpackHash()

	case 17: // zset listpack
		return r.listpackZSet()

	case 20: // set listpack
		return r.listpackSet()

	default:
		return nil, fmt.Errorf("%w: type %d", ErrRDBUnsupported, kind)
	}
}

func (r *rdbReader) legacyDouble() (float64, error) {
	size, err := r.readByte()
	if err != nil {
		return 0, err
	}
	switch size {
	case 253:
		return math.NaN(), nil
	case 254:
		return math.Inf(1), nil
	case 255:
		return math.Inf(-1), nil
	}
	buf, err := r.readFull(int(size))
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(string(buf), 64)
}

func (r *rdbReader) intset() (value, error) {
	blob, err := r.string()
	if err != nil {
		return nil, err
	}
	if len(blob) < 8 {
		return nil, ErrRDBFormat
	}
	reader := bytes.NewReader(blob)

	var encodingRaw, countRaw [4]byte
	if _, err := io.ReadFull(reader, encodingRaw[:]); err != nil {
		return nil, ErrRDBFormat
	}
	if _, err := io.ReadFull(reader, countRaw[:]); err != nil {
		return nil, ErrRDBFormat
	}
	encoding := binary.LittleEndian.Uint32(encodingRaw[:])
	count := binary.LittleEndian.Uint32(countRaw[:])
	if int64(count) > int64(len(blob)) {
		return nil, ErrRDBFormat
	}

	members := make(map[string]struct{}, count)
	for i := uint32(0); i < count; i++ {
		var (
			parsed int64
			buf    [8]byte
		)
		switch encoding {
		case 2:
			if _, err := io.ReadFull(reader, buf[:2]); err != nil {
				return nil, ErrRDBFormat
			}
			parsed = int64(int16(binary.LittleEndian.Uint16(buf[:2])))
		case 4:
			if _, err := io.ReadFull(reader, buf[:4]); err != nil {
				return nil, ErrRDBFormat
			}
			parsed = int64(int32(binary.LittleEndian.Uint32(buf[:4])))
		case 8:
			if _, err := io.ReadFull(reader, buf[:8]); err != nil {
				return nil, ErrRDBFormat
			}
			parsed = int64(binary.LittleEndian.Uint64(buf[:8]))
		default:
			return nil, fmt.Errorf("%w: intset encoding %d", ErrRDBUnsupported, encoding)
		}
		members[strconv.FormatInt(parsed, 10)] = struct{}{}
	}
	return &setValue{members: members}, nil
}

func (r *rdbReader) quicklist() (value, error) {
	count, _, err := r.length()
	if err != nil {
		return nil, err
	}
	if err := r.checkCount(count); err != nil {
		return nil, err
	}
	list := newListValue()
	for i := 0; i < count; i++ {
		// Each node is a container kind followed by its payload: 1 = a single
		// plain element, 2 = a packed listpack.
		container, _, err := r.length()
		if err != nil {
			return nil, err
		}
		node, err := r.string()
		if err != nil {
			return nil, err
		}
		if container == 1 {
			list.pushBack(node)
			continue
		}
		items, err := decodeListpack(node)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			list.pushBack(item)
		}
	}
	return list, nil
}

func (r *rdbReader) listpackHash() (value, error) {
	packed, err := r.string()
	if err != nil {
		return nil, err
	}
	items, err := decodeListpack(packed)
	if err != nil {
		return nil, err
	}
	fields := make(map[string][]byte, len(items)/2)
	for i := 0; i+1 < len(items); i += 2 {
		fields[string(items[i])] = items[i+1]
	}
	return &hashValue{fields: fields}, nil
}

func (r *rdbReader) listpackZSet() (value, error) {
	packed, err := r.string()
	if err != nil {
		return nil, err
	}
	items, err := decodeListpack(packed)
	if err != nil {
		return nil, err
	}
	zv := newZSetValue()
	for i := 0; i+1 < len(items); i += 2 {
		score, err := strconv.ParseFloat(string(items[i+1]), 64)
		if err != nil {
			return nil, ErrRDBFormat
		}
		member := string(items[i])
		zv.dict[member] = score
		zv.list.insert(member, score)
	}
	return zv, nil
}

func (r *rdbReader) listpackSet() (value, error) {
	packed, err := r.string()
	if err != nil {
		return nil, err
	}
	items, err := decodeListpack(packed)
	if err != nil {
		return nil, err
	}
	members := make(map[string]struct{}, len(items))
	for _, item := range items {
		members[string(item)] = struct{}{}
	}
	return &setValue{members: members}, nil
}

// decodeListpack decodes a Redis listpack into its elements.
func decodeListpack(data []byte) ([][]byte, error) {
	if len(data) < 7 {
		return nil, ErrRDBFormat
	}
	pos := 6 // 4-byte total length + 2-byte element count
	var out [][]byte

	for pos < len(data) {
		first := data[pos]
		if first == 0xFF {
			return out, nil
		}

		var (
			value    []byte
			entryLen int
		)
		switch {
		case first&0x80 == 0: // 7-bit unsigned
			value = []byte(strconv.FormatUint(uint64(first), 10))
			entryLen = 1

		case first&0xC0 == 0x80: // 6-bit string
			length := int(first & 0x3F)
			if pos+1+length > len(data) {
				return nil, ErrRDBFormat
			}
			value = data[pos+1 : pos+1+length]
			entryLen = 1 + length

		case first&0xE0 == 0xC0: // 13-bit signed
			if pos+2 > len(data) {
				return nil, ErrRDBFormat
			}
			signed := (int(first&0x1F) << 8) | int(data[pos+1])
			if signed >= 1<<12 {
				signed -= 1 << 13
			}
			value = []byte(strconv.Itoa(signed))
			entryLen = 2

		case first&0xF0 == 0xE0: // 12-bit string
			if pos+2 > len(data) {
				return nil, ErrRDBFormat
			}
			length := (int(first&0x0F) << 8) | int(data[pos+1])
			if pos+2+length > len(data) {
				return nil, ErrRDBFormat
			}
			value = data[pos+2 : pos+2+length]
			entryLen = 2 + length

		case first == 0xF0: // 32-bit string
			if pos+5 > len(data) {
				return nil, ErrRDBFormat
			}
			length := int(binary.LittleEndian.Uint32(data[pos+1 : pos+5]))
			if pos+5+length > len(data) {
				return nil, ErrRDBFormat
			}
			value = data[pos+5 : pos+5+length]
			entryLen = 5 + length

		case first == 0xF1: // 16-bit signed
			if pos+3 > len(data) {
				return nil, ErrRDBFormat
			}
			value = []byte(strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(data[pos+1:pos+3]))), 10))
			entryLen = 3

		case first == 0xF2: // 24-bit signed
			if pos+4 > len(data) {
				return nil, ErrRDBFormat
			}
			raw := int64(data[pos+1]) | int64(data[pos+2])<<8 | int64(data[pos+3])<<16
			if raw&0x800000 != 0 {
				raw -= 1 << 24
			}
			value = []byte(strconv.FormatInt(raw, 10))
			entryLen = 4

		case first == 0xF3: // 32-bit signed
			if pos+5 > len(data) {
				return nil, ErrRDBFormat
			}
			value = []byte(strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(data[pos+1:pos+5]))), 10))
			entryLen = 5

		case first == 0xF4: // 64-bit signed
			if pos+9 > len(data) {
				return nil, ErrRDBFormat
			}
			value = []byte(strconv.FormatInt(int64(binary.LittleEndian.Uint64(data[pos+1:pos+9])), 10))
			entryLen = 9

		default:
			return nil, fmt.Errorf("%w: listpack entry 0x%02x", ErrRDBUnsupported, first)
		}

		out = append(out, append([]byte(nil), value...))
		pos += entryLen + listpackBacklenSize(entryLen)
	}

	return nil, ErrRDBFormat
}

func listpackBacklenSize(entryLen int) int {
	switch {
	case entryLen <= 127:
		return 1
	case entryLen < 16384:
		return 2
	case entryLen < 1<<21:
		return 3
	case entryLen < 1<<28:
		return 4
	default:
		return 5
	}
}

// lzfDecompress expands Redis' LZF-compressed strings.
func lzfDecompress(input []byte, outputLen int) ([]byte, error) {
	out := make([]byte, outputLen)
	in, o := 0, 0

	for in < len(input) {
		control := int(input[in])
		in++

		if control < 32 {
			run := control + 1
			if in+run > len(input) || o+run > len(out) {
				return nil, ErrRDBFormat
			}
			copy(out[o:], input[in:in+run])
			in += run
			o += run
			continue
		}

		run := control >> 5
		if run == 7 {
			if in >= len(input) {
				return nil, ErrRDBFormat
			}
			run += int(input[in])
			in++
		}
		if in >= len(input) {
			return nil, ErrRDBFormat
		}
		reference := o - ((control & 0x1F) << 8) - int(input[in]) - 1
		in++
		if reference < 0 {
			return nil, ErrRDBFormat
		}
		for i := 0; i < run+2; i++ {
			if o >= len(out) {
				return nil, ErrRDBFormat
			}
			out[o] = out[reference]
			o++
			reference++
		}
	}

	if o != outputLen {
		return nil, ErrRDBFormat
	}
	return out, nil
}
