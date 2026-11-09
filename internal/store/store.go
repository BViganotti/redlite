// Package store implements the in-memory keyspace, expiration, and the
// append-only file.
//
// Every value carries an explicit type. Commands that touch a key with the
// wrong type return ErrWrongType instead of guessing, and the type is what TYPE
// and the append-only-file rewriter act on.
//
// The keyspace is protected by a single reader-writer mutex. That is the
// simplest thing that is correct; sharding is tracked in ROADMAP.md as a
// performance task rather than a correctness one.
package store

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by value-changing operations. Command handlers turn these
// into Redis error replies.
var (
	ErrNotInteger = errors.New("value is not an integer or out of range")
	ErrOverflow   = errors.New("increment or decrement would overflow")
	ErrWrongType  = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
)

type entry struct {
	value    value
	expireAt int64 // unix milliseconds; 0 means no expiry
	version  uint64
}

// Stats are counters that INFO reports.
type Stats struct {
	Hits     uint64
	Misses   uint64
	Expired  uint64
	SetCount uint64
}

// Store is a concurrent keyspace.
type Store struct {
	mu   sync.RWMutex
	data map[string]*entry

	hits    atomic.Uint64
	misses  atomic.Uint64
	expired atomic.Uint64
	sets    atomic.Uint64

	nextVersion atomic.Uint64
	epoch       atomic.Uint64

	waiters    *waiterSet
	expireHook func(string)

	now      func() int64
	stop     chan struct{}
	stopOnce sync.Once
}

// New returns an empty store using the wall clock.
func New() *Store {
	return NewWithClock(func() int64 { return time.Now().UnixMilli() })
}

// NewWithClock returns a store driven by an injectable clock, for tests.
func NewWithClock(now func() int64) *Store {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Store{
		data:    make(map[string]*entry),
		now:     now,
		stop:    make(chan struct{}),
		waiters: newWaiterSet(),
	}
}

// StartExpiry runs the background expiration sampler. Redis samples a few keys
// per tick instead of scanning the whole keyspace; so do we.
func (s *Store) StartExpiry(interval time.Duration, sample int) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	if sample <= 0 {
		sample = 20
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.expireSample(sample)
			case <-s.stop:
				return
			}
		}
	}()
}

// Close stops the background expiration sampler. It is safe to call twice.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Store) expireSample(limit int) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := 0
	for key, e := range s.data {
		if seen >= limit {
			return
		}
		seen++
		if isExpired(e, now) {
			delete(s.data, key)
			s.expired.Add(1)
			s.fireExpire(key)
		}
	}
}

// Stats returns a snapshot of the counters.
func (s *Store) Stats() Stats {
	return Stats{
		Hits:     s.hits.Load(),
		Misses:   s.misses.Load(),
		Expired:  s.expired.Load(),
		SetCount: s.sets.Load(),
	}
}

// ResetStats zeroes the counters reported by INFO.
func (s *Store) ResetStats() {
	s.hits.Store(0)
	s.misses.Store(0)
	s.expired.Store(0)
	s.sets.Store(0)
}

// lookupRead returns a live entry. The caller must hold at least a read lock.
// Expired entries are reported as missing but not deleted, because deletion
// requires the write lock.
func (s *Store) lookupRead(key string, now int64) (*entry, bool) {
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		return nil, false
	}
	return e, true
}

// lookupWrite returns a live entry and removes it if it has expired. The caller
// must hold the write lock.
func (s *Store) lookupWrite(key string, now int64) (*entry, bool) {
	e, ok := s.data[key]
	if !ok {
		return nil, false
	}
	if isExpired(e, now) {
		delete(s.data, key)
		s.expired.Add(1)
		s.fireExpire(key)
		return nil, false
	}
	return e, true
}

// SetExpireHook installs a callback invoked when a key expires. It must not
// block: it runs while the keyspace lock is held.
func (s *Store) SetExpireHook(fn func(string)) {
	s.mu.Lock()
	s.expireHook = fn
	s.mu.Unlock()
}

func (s *Store) fireExpire(key string) {
	if s.expireHook != nil {
		s.expireHook(key)
	}
}

// WatchState is what WATCH records for a key. Two states compare equal only
// when the key's existence, version and flush epoch are unchanged.
type WatchState struct {
	Version uint64
	Exists  bool
	Epoch   uint64
}

// Watch returns the current state of a key for optimistic locking.
func (s *Store) Watch(key string) WatchState {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookupRead(key, now)
	if !ok {
		return WatchState{Epoch: s.epoch.Load()}
	}
	return WatchState{Version: e.version, Exists: true, Epoch: s.epoch.Load()}
}

// stampKey assigns a fresh version to a key after a modification. The caller
// must hold the write lock.
func (s *Store) stampKey(key string) {
	if e, ok := s.data[key]; ok {
		e.version = s.nextVersion.Add(1)
	}
}

// Get returns a copy of the string stored at key.
func (s *Store) Get(key string) ([]byte, bool, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.lookupRead(key, now)
	if !ok {
		s.misses.Add(1)
		return nil, false, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return nil, false, ErrWrongType
	}
	s.hits.Add(1)
	return clone(sv.data), true, nil
}

// SetOptions controls the behaviour of Set.
type SetOptions struct {
	NX        bool
	XX        bool
	KeepTTL   bool
	HasExpire bool
	ExpireAt  int64 // unix milliseconds
	// Get asks for the previous string value, and makes Set reject keys of
	// another type the way SET ... GET does.
	Get bool
}

// SetResult reports what Set did and what it replaced.
type SetResult struct {
	Written bool
	Old     []byte
	HadOld  bool
}

// Set stores value at key, replacing a value of any type unless Get is set.
func (s *Store) Set(key string, value []byte, opts SetOptions) (SetResult, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.lookupWrite(key, now)

	var res SetResult
	if exists {
		sv, ok := current.value.(*stringValue)
		if opts.Get && !ok {
			return SetResult{}, ErrWrongType
		}
		if ok {
			res.Old = clone(sv.data)
			res.HadOld = true
		}
	}
	if (opts.NX && exists) || (opts.XX && !exists) {
		return res, nil
	}

	expireAt := int64(0)
	switch {
	case opts.KeepTTL && exists:
		expireAt = current.expireAt
	case opts.HasExpire:
		expireAt = opts.ExpireAt
	}

	res.Written = true
	s.sets.Add(1)

	if expireAt != 0 && expireAt <= now {
		delete(s.data, key)
		return res, nil
	}
	s.data[key] = &entry{
		value:    &stringValue{data: clone(value)},
		expireAt: expireAt,
	}
	s.stampKey(key)
	return res, nil
}

// KeyValue is a key/value pair used by MSET-style and HSET-style operations.
type KeyValue struct {
	Key   string
	Value []byte
}

// SetMany writes every pair under one lock so the batch is atomic to readers.
// It replaces values of any type, as MSET does.
func (s *Store) SetMany(pairs []KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		s.data[pair.Key] = &entry{value: &stringValue{data: clone(pair.Value)}}
		s.stampKey(pair.Key)
	}
	s.sets.Add(uint64(len(pairs)))
}

// SetManyNX writes all pairs only if none of the keys exists.
func (s *Store) SetManyNX(pairs []KeyValue) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		if _, ok := s.lookupWrite(pair.Key, now); ok {
			return false
		}
	}
	for _, pair := range pairs {
		s.data[pair.Key] = &entry{value: &stringValue{data: clone(pair.Value)}}
		s.stampKey(pair.Key)
	}
	s.sets.Add(uint64(len(pairs)))
	return true
}

// Del removes keys and reports how many existed and were not already expired.
func (s *Store) Del(keys []string) int64 {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int64
	for _, key := range keys {
		e, ok := s.data[key]
		if !ok {
			continue
		}
		delete(s.data, key)
		if isExpired(e, now) {
			s.expired.Add(1)
			continue
		}
		removed++
	}
	return removed
}

// Exists counts how many of keys exist, once per occurrence.
func (s *Store) Exists(keys []string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	var n int64
	for _, key := range keys {
		if _, ok := s.lookupRead(key, now); ok {
			n++
		}
	}
	return n
}

// Type reports the value type of key, or "none".
func (s *Store) Type(key string) string {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookupRead(key, now)
	if !ok {
		return "none"
	}
	return e.value.kind().String()
}

// Append appends suffix to the string at key and returns the new length.
func (s *Store) Append(key string, suffix []byte) (int, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.lookupWrite(key, now)
	if !ok {
		s.data[key] = &entry{value: &stringValue{data: clone(suffix)}}
		s.stampKey(key)
		s.sets.Add(1)
		return len(suffix), nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}
	sv.data = append(sv.data, suffix...)
	s.stampKey(key)
	return len(sv.data), nil
}

// StrLen returns the length of the string at key.
func (s *Store) StrLen(key string) (int64, error) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookupRead(key, now)
	if !ok {
		return 0, nil
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return 0, ErrWrongType
	}
	return int64(len(sv.data)), nil
}

// IncrBy adds delta to the integer stored at key, initializing missing keys to
// zero.
func (s *Store) IncrBy(key string, delta int64) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.lookupWrite(key, now)
	var current int64
	if ok {
		sv, isString := e.value.(*stringValue)
		if !isString {
			return 0, ErrWrongType
		}
		parsed, err := strconv.ParseInt(string(sv.data), 10, 64)
		if err != nil {
			return 0, ErrNotInteger
		}
		current = parsed
	}
	if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
		return 0, ErrOverflow
	}

	next := current + delta
	if ok {
		sv := e.value.(*stringValue)
		sv.data = strconv.AppendInt(sv.data[:0], next, 10)
	} else {
		s.data[key] = &entry{value: &stringValue{data: strconv.AppendInt(nil, next, 10)}}
		s.sets.Add(1)
	}
	s.stampKey(key)
	return next, nil
}

// ExpireMode selects the conditional behaviour of Expire.
type ExpireMode int

const (
	ExpireAlways ExpireMode = iota
	ExpireNX
	ExpireXX
	ExpireGT
	ExpireLT
)

// Expire sets the absolute expiry of key to expireAt (unix milliseconds).
func (s *Store) Expire(key string, expireAt int64, mode ExpireMode) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.lookupWrite(key, now)
	if !ok {
		return false
	}

	switch mode {
	case ExpireNX:
		if e.expireAt != 0 {
			return false
		}
	case ExpireXX:
		if e.expireAt == 0 {
			return false
		}
	case ExpireGT:
		if e.expireAt == 0 || expireAt <= e.expireAt {
			return false
		}
	case ExpireLT:
		if e.expireAt != 0 && expireAt >= e.expireAt {
			return false
		}
	}

	if expireAt <= now {
		delete(s.data, key)
		s.expired.Add(1)
		s.fireExpire(key)
		return true
	}
	e.expireAt = expireAt
	s.stampKey(key)
	return true
}

// Expiry retry codes returned alongside TTLMillis.
const (
	ExpiryMissing  = -2
	ExpiryNoExpiry = -1
)

// TTLMillis returns the remaining lifetime of key in milliseconds, or one of
// the Expiry constants.
func (s *Store) TTLMillis(key string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookupRead(key, now)
	if !ok {
		return ExpiryMissing
	}
	if e.expireAt == 0 {
		return ExpiryNoExpiry
	}
	return e.expireAt - now
}

// ExpireTimeMillis returns the absolute expiry of key in milliseconds, or one
// of the Expiry constants.
func (s *Store) ExpireTimeMillis(key string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookupRead(key, now)
	if !ok {
		return ExpiryMissing
	}
	if e.expireAt == 0 {
		return ExpiryNoExpiry
	}
	return e.expireAt
}

// Persist removes the expiry from key.
func (s *Store) Persist(key string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupWrite(key, now)
	if !ok {
		return false
	}
	if e.expireAt == 0 {
		return false
	}
	e.expireAt = 0
	s.stampKey(key)
	return true
}

// Keys returns every live key matching pattern.
func (s *Store) Keys(pattern string) []string {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.data))
	for key, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		if pattern == "*" || Match(pattern, key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Scan walks the keyspace from cursor, returning the next cursor and up to
// count matching keys.
//
// The cursor is an index into a sorted snapshot of the keyspace. That keeps
// pagination correct and deterministic, at the cost of sorting on every call;
// a bucket-based cursor is tracked in ROADMAP.md.
func (s *Store) Scan(cursor uint64, pattern string, count int, typeName string) (uint64, []string) {
	if count <= 0 {
		count = 10
	}
	now := s.now()
	s.mu.RLock()
	keys := make([]string, 0, len(s.data))
	for key, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		if typeName != "" && typeName != "string" && typeName != e.value.kind().String() {
			continue
		}
		keys = append(keys, key)
	}
	s.mu.RUnlock()
	sort.Strings(keys)

	if cursor >= uint64(len(keys)) {
		return 0, nil
	}

	var (
		out      []string
		examined int
	)
	for examined < count && cursor < uint64(len(keys)) {
		key := keys[cursor]
		cursor++
		examined++
		if pattern != "" && pattern != "*" && !Match(pattern, key) {
			continue
		}
		out = append(out, key)
	}
	if cursor >= uint64(len(keys)) {
		cursor = 0
	}
	return cursor, out
}

// RandomKey returns an arbitrary live key, if one exists.
func (s *Store) RandomKey() (string, bool) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	for key, e := range s.data {
		if !isExpired(e, now) {
			return key, true
		}
	}
	return "", false
}

// Rename moves src to dst. It reports false if src does not exist.
func (s *Store) Rename(src, dst string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.lookupWrite(src, now)
	if !ok {
		return false
	}
	delete(s.data, src)
	s.data[dst] = e
	s.stampKey(dst)
	return true
}

// RenameNX moves src to dst only if dst does not exist.
func (s *Store) RenameNX(src, dst string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.lookupWrite(src, now)
	if !ok {
		return false
	}
	if _, exists := s.lookupWrite(dst, now); exists {
		return false
	}
	delete(s.data, src)
	s.data[dst] = e
	s.stampKey(dst)
	return true
}

// DBSize counts keys that are not expired.
func (s *Store) DBSize() int64 {
	keys, _ := s.KeyCounts()
	return keys
}

// KeyCounts reports the number of live keys and how many of them expire.
func (s *Store) KeyCounts() (keys, expiring int64) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		keys++
		if e.expireAt != 0 {
			expiring++
		}
	}
	return keys, expiring
}

// Flush removes every key.
func (s *Store) Flush() {
	s.mu.Lock()
	s.data = make(map[string]*entry)
	s.epoch.Add(1)
	s.mu.Unlock()
}

// EmitSnapshot calls emit with canonical commands that recreate the keyspace.
// The append-only-file rewriter uses it; emit must not retain the slices.
func (s *Store) EmitSnapshot(emit func(args [][]byte)) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	for key, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		switch v := e.value.(type) {
		case *stringValue:
			cmd := [][]byte{[]byte("SET"), []byte(key), clone(v.data)}
			if e.expireAt != 0 {
				cmd = append(cmd, []byte("PXAT"), []byte(strconv.FormatInt(e.expireAt, 10)))
			}
			emit(cmd)
		case *hashValue:
			fields := sortedKeys(v.fields)
			for start := 0; start < len(fields); start += snapshotChunk {
				end := min(start+snapshotChunk, len(fields))
				cmd := [][]byte{[]byte("HSET"), []byte(key)}
				for _, field := range fields[start:end] {
					cmd = append(cmd, []byte(field), clone(v.fields[field]))
				}
				emit(cmd)
			}
			emitExpiry(emit, key, e.expireAt)
		case *listValue:
			items := v.toSlice()
			for start := 0; start < len(items); start += snapshotChunk {
				end := min(start+snapshotChunk, len(items))
				cmd := [][]byte{[]byte("RPUSH"), []byte(key)}
				for _, item := range items[start:end] {
					cmd = append(cmd, clone(item))
				}
				emit(cmd)
			}
			emitExpiry(emit, key, e.expireAt)
		case *setValue:
			members := sortedSetKeys(v.members)
			for start := 0; start < len(members); start += snapshotChunk {
				end := min(start+snapshotChunk, len(members))
				cmd := [][]byte{[]byte("SADD"), []byte(key)}
				for _, member := range members[start:end] {
					cmd = append(cmd, []byte(member))
				}
				emit(cmd)
			}
			emitExpiry(emit, key, e.expireAt)
		case *zsetValue:
			members := v.list.all()
			for start := 0; start < len(members); start += snapshotChunk {
				end := min(start+snapshotChunk, len(members))
				cmd := [][]byte{[]byte("ZADD"), []byte(key)}
				for _, member := range members[start:end] {
					cmd = append(cmd, []byte(FormatScore(member.Score)), []byte(member.Member))
				}
				emit(cmd)
			}
			emitExpiry(emit, key, e.expireAt)
		}
	}
}

const snapshotChunk = 128

func emitExpiry(emit func(args [][]byte), key string, expireAt int64) {
	if expireAt == 0 {
		return
	}
	emit([][]byte{
		[]byte("PEXPIREAT"),
		[]byte(key),
		[]byte(strconv.FormatInt(expireAt, 10)),
	})
}

func isExpired(e *entry, now int64) bool {
	return e.expireAt != 0 && e.expireAt <= now
}

func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedSetKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
