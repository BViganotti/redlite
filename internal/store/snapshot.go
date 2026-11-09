package store

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"sort"
)

// Snapshot format (not Redis' RDB):
//
//	"REDLITE1"        magic
//	uint32            format version
//	uint64            number of entries
//	entries...
//	uint32            CRC-32 (IEEE) of everything above
//
// Every string is a uint32 length followed by the bytes. Expiry is a signed
// 64-bit unix-millisecond deadline, zero meaning none.
const (
	snapshotMagic   = "REDLITE1"
	snapshotVersion = 1
	maxSnapshotSize = 2 << 30
)

// Snapshot errors.
var (
	ErrSnapshotCorrupt = errors.New("snapshot is corrupt")
	ErrSnapshotVersion = errors.New("unsupported snapshot version")
	ErrSnapshotTooBig  = errors.New("snapshot exceeds the size limit")
)

type snapshotEntry struct {
	kind     ValueType
	key      string
	expireAt int64
	str      []byte
	hash     map[string][]byte
	list     [][]byte
	set      []string
	zset     []ScoreMember
}

// Save writes a snapshot of the live keyspace. It copies the data first and
// serialises outside the lock, so writers are only blocked by the copy.
func (s *Store) Save(w io.Writer) error {
	entries := s.snapshotEntries()

	checksum := crc32.NewIEEE()
	buffered := bufio.NewWriterSize(io.MultiWriter(w, checksum), 64*1024)

	if _, err := buffered.WriteString(snapshotMagic); err != nil {
		return err
	}
	if err := writeUint32(buffered, snapshotVersion); err != nil {
		return err
	}
	if err := writeUint64(buffered, uint64(len(entries))); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := encodeSnapshotEntry(buffered, entry); err != nil {
			return err
		}
	}
	if err := buffered.Flush(); err != nil {
		return err
	}

	var tail [4]byte
	binary.BigEndian.PutUint32(tail[:], checksum.Sum32())
	_, err := w.Write(tail[:])
	return err
}

func (s *Store) snapshotEntries() []snapshotEntry {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	entries := make([]snapshotEntry, 0, 32)
	for _, sh := range s.shards {
		for key, e := range sh.data {
			if isExpired(e, now) {
				continue
			}
			snap := snapshotEntry{key: key, kind: e.value.kind(), expireAt: e.expireAt}
			switch v := e.value.(type) {
			case *stringValue:
				snap.str = clone(v.data)
			case *hashValue:
				snap.hash = make(map[string][]byte, len(v.fields))
				for field, value := range v.fields {
					snap.hash[field] = clone(value)
				}
			case *listValue:
				items := v.toSlice()
				snap.list = make([][]byte, len(items))
				for i, item := range items {
					snap.list[i] = clone(item)
				}
			case *setValue:
				snap.set = sortedSetKeys(v.members)
			case *zsetValue:
				snap.zset = v.list.all()
			}
			entries = append(entries, snap)
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries
}

func encodeSnapshotEntry(w io.Writer, entry snapshotEntry) error {
	if err := writeUint8(w, byte(entry.kind)); err != nil {
		return err
	}
	if err := writeBytes(w, []byte(entry.key)); err != nil {
		return err
	}
	if err := writeUint64(w, uint64(entry.expireAt)); err != nil {
		return err
	}

	switch entry.kind {
	case TypeString:
		return writeBytes(w, entry.str)

	case TypeHash:
		if err := writeUint32(w, uint32(len(entry.hash))); err != nil {
			return err
		}
		for _, field := range sortedKeys(entry.hash) {
			if err := writeBytes(w, []byte(field)); err != nil {
				return err
			}
			if err := writeBytes(w, entry.hash[field]); err != nil {
				return err
			}
		}

	case TypeList:
		if err := writeUint32(w, uint32(len(entry.list))); err != nil {
			return err
		}
		for _, item := range entry.list {
			if err := writeBytes(w, item); err != nil {
				return err
			}
		}

	case TypeSet:
		if err := writeUint32(w, uint32(len(entry.set))); err != nil {
			return err
		}
		for _, member := range entry.set {
			if err := writeBytes(w, []byte(member)); err != nil {
				return err
			}
		}

	case TypeZSet:
		if err := writeUint32(w, uint32(len(entry.zset))); err != nil {
			return err
		}
		for _, member := range entry.zset {
			if err := writeUint64(w, math.Float64bits(member.Score)); err != nil {
				return err
			}
			if err := writeBytes(w, []byte(member.Member)); err != nil {
				return err
			}
		}

	default:
		return ErrSnapshotCorrupt
	}
	return nil
}

// Load replaces the keyspace with the snapshot in r. The existing data is only
// replaced after the whole snapshot has been parsed and verified.
func (s *Store) Load(r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxSnapshotSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxSnapshotSize {
		return ErrSnapshotTooBig
	}
	if len(data) < len(snapshotMagic)+4+8+4 {
		return ErrSnapshotCorrupt
	}

	body := data[:len(data)-4]
	want := binary.BigEndian.Uint32(data[len(data)-4:])
	if crc32.ChecksumIEEE(body) != want {
		return ErrSnapshotCorrupt
	}

	reader := bytes.NewReader(body)
	magic := make([]byte, len(snapshotMagic))
	if _, err := io.ReadFull(reader, magic); err != nil {
		return ErrSnapshotCorrupt
	}
	if string(magic) != snapshotMagic {
		return ErrSnapshotCorrupt
	}
	version, err := readUint32(reader)
	if err != nil {
		return ErrSnapshotCorrupt
	}
	if version != snapshotVersion {
		return ErrSnapshotVersion
	}
	count, err := readUint64(reader)
	if err != nil {
		return ErrSnapshotCorrupt
	}
	if count > uint64(reader.Len()) {
		return ErrSnapshotCorrupt
	}

	now := s.now()
	parsed := make(map[string]*entry, int(count))
	for i := uint64(0); i < count; i++ {
		key, entry, err := decodeSnapshotEntry(reader)
		if err != nil {
			return err
		}
		if entry.expireAt != 0 && entry.expireAt <= now {
			continue
		}
		parsed[key] = entry
	}
	if reader.Len() != 0 {
		return ErrSnapshotCorrupt
	}

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

func decodeSnapshotEntry(r *bytes.Reader) (string, *entry, error) {
	kind, err := r.ReadByte()
	if err != nil {
		return "", nil, ErrSnapshotCorrupt
	}
	key, err := readString(r)
	if err != nil {
		return "", nil, err
	}
	expireRaw, err := readUint64(r)
	if err != nil {
		return "", nil, err
	}
	result := &entry{expireAt: int64(expireRaw)}

	switch ValueType(kind) {
	case TypeString:
		value, err := readBytes(r)
		if err != nil {
			return "", nil, err
		}
		result.value = &stringValue{data: value}

	case TypeHash:
		count, err := readCount(r)
		if err != nil {
			return "", nil, err
		}
		fields := make(map[string][]byte, count)
		for i := 0; i < count; i++ {
			field, err := readString(r)
			if err != nil {
				return "", nil, err
			}
			value, err := readBytes(r)
			if err != nil {
				return "", nil, err
			}
			fields[field] = value
		}
		result.value = &hashValue{fields: fields}

	case TypeList:
		count, err := readCount(r)
		if err != nil {
			return "", nil, err
		}
		list := newListValue()
		for i := 0; i < count; i++ {
			item, err := readBytes(r)
			if err != nil {
				return "", nil, err
			}
			list.pushBack(item)
		}
		result.value = list

	case TypeSet:
		count, err := readCount(r)
		if err != nil {
			return "", nil, err
		}
		members := make(map[string]struct{}, count)
		for i := 0; i < count; i++ {
			member, err := readString(r)
			if err != nil {
				return "", nil, err
			}
			members[member] = struct{}{}
		}
		result.value = &setValue{members: members}

	case TypeZSet:
		count, err := readCount(r)
		if err != nil {
			return "", nil, err
		}
		entries := make([]ScoreMember, 0, count)
		for i := 0; i < count; i++ {
			raw, err := readUint64(r)
			if err != nil {
				return "", nil, err
			}
			member, err := readString(r)
			if err != nil {
				return "", nil, err
			}
			entries = append(entries, ScoreMember{Score: math.Float64frombits(raw), Member: member})
		}
		zv := newZSetValue()
		for _, item := range entries {
			zv.dict[item.Member] = item.Score
			zv.list.insert(item.Member, item.Score)
		}
		result.value = zv

	default:
		return "", nil, ErrSnapshotCorrupt
	}
	return key, result, nil
}

func writeUint8(w io.Writer, value byte) error {
	_, err := w.Write([]byte{value})
	return err
}

func writeUint32(w io.Writer, value uint32) error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], value)
	_, err := w.Write(buf[:])
	return err
}

func writeUint64(w io.Writer, value uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	_, err := w.Write(buf[:])
	return err
}

func writeBytes(w io.Writer, data []byte) error {
	if err := writeUint32(w, uint32(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readUint32(r *bytes.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, ErrSnapshotCorrupt
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func readUint64(r *bytes.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, ErrSnapshotCorrupt
	}
	return binary.BigEndian.Uint64(buf[:]), nil
}

func readBytes(r *bytes.Reader) ([]byte, error) {
	size, err := readUint32(r)
	if err != nil {
		return nil, err
	}
	if uint64(size) > uint64(r.Len()) {
		return nil, ErrSnapshotCorrupt
	}
	out := make([]byte, size)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, ErrSnapshotCorrupt
	}
	return out, nil
}

func readString(r *bytes.Reader) (string, error) {
	data, err := readBytes(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func readCount(r *bytes.Reader) (int, error) {
	value, err := readUint32(r)
	if err != nil {
		return 0, err
	}
	// Every element costs at least one byte, so a count larger than the
	// remaining payload is corrupt.
	if uint64(value) > uint64(r.Len()) {
		return 0, ErrSnapshotCorrupt
	}
	return int(value), nil
}
