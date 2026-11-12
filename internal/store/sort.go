package store

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrSortNotDouble reports a non-numeric element in a numeric SORT.
var ErrSortNotDouble = errors.New("One or more scores can't be converted into double")

// SortOptions is a parsed SORT request.
type SortOptions struct {
	ByPattern   string
	GetPatterns []string
	Limit       bool
	Offset      int64
	Count       int64
	Desc        bool
	Alpha       bool
	Store       string
}

// SortItem is one output element. Missing reports a GET lookup that found no
// value (rendered as a null reply, or the empty string when stored).
type SortItem struct {
	Value   []byte
	Missing bool
}

type sortElement struct {
	element string
	weight  string
	score   float64
	missing bool
}

// Sort evaluates SORT, returning the output elements and writing them to
// opts.Store when set. The whole operation holds every shard so the source and
// destination form a single atomic step.
func (s *Store) Sort(key string, opts SortOptions) ([]SortItem, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	source, err := s.sortSource(key, now)
	if err != nil {
		return nil, err
	}

	dontsort := opts.ByPattern != "" && !strings.Contains(opts.ByPattern, "*")
	alpha := opts.Alpha
	byPattern := opts.ByPattern
	if dontsort && opts.Store != "" {
		if e, ok := s.lookupRead(s.shardFor(key), key, now); ok {
			if _, isSet := e.value.(*setValue); isSet {
				dontsort = false
				alpha = true
				byPattern = ""
			}
		}
	}

	vector := make([]sortElement, len(source))
	for i, element := range source {
		vector[i].element = element
	}

	if !dontsort {
		for i := range vector {
			weight := vector[i].element
			if byPattern != "" {
				value, ok := s.lookupPattern(byPattern, vector[i].element, now)
				vector[i].missing = !ok
				if ok {
					weight = string(value)
				} else {
					// Redis leaves the default score of zero when a BY lookup
					// misses, without trying to parse the empty string.
					weight = ""
				}
			}
			vector[i].weight = weight
			if !alpha {
				if vector[i].missing {
					vector[i].score = 0
					continue
				}
				parsed, err := strconv.ParseFloat(weight, 64)
				if err != nil {
					return nil, ErrSortNotDouble
				}
				vector[i].score = parsed
			}
		}

		sort.SliceStable(vector, func(i, j int) bool {
			cmp := 0
			if alpha {
				cmp = strings.Compare(vector[i].weight, vector[j].weight)
			} else if vector[i].score < vector[j].score {
				cmp = -1
			} else if vector[i].score > vector[j].score {
				cmp = 1
			}
			if opts.Desc {
				cmp = -cmp
			}
			return cmp < 0
		})
	} else if opts.Desc {
		for i, j := 0, len(vector)-1; i < j; i, j = i+1, j-1 {
			vector[i], vector[j] = vector[j], vector[i]
		}
	}

	start := int(opts.Offset)
	if start < 0 {
		start = 0
	}
	if start > len(vector) {
		start = len(vector)
	}
	end := len(vector)
	if opts.Limit && opts.Count >= 0 {
		// Compare before converting, so a huge COUNT cannot overflow the
		// addition and produce a negative slice bound.
		if opts.Count < int64(end-start) {
			end = start + int(opts.Count)
		}
	}

	var out []SortItem
	if len(opts.GetPatterns) == 0 {
		out = make([]SortItem, 0, end-start)
		for _, item := range vector[start:end] {
			out = append(out, SortItem{Value: []byte(item.element)})
		}
	} else {
		out = make([]SortItem, 0, (end-start)*len(opts.GetPatterns))
		for _, item := range vector[start:end] {
			for _, pattern := range opts.GetPatterns {
				value, ok := s.lookupPattern(pattern, item.element, now)
				if !ok {
					out = append(out, SortItem{Missing: true})
					continue
				}
				out = append(out, SortItem{Value: clone(value)})
			}
		}
	}

	if opts.Store != "" {
		destShard := s.shardFor(opts.Store)
		if len(out) == 0 {
			delete(destShard.data, opts.Store)
		} else {
			list := newListValue()
			for _, item := range out {
				if item.Missing {
					list.pushBack([]byte{})
				} else {
					list.pushBack(clone(item.Value))
				}
			}
			destShard.data[opts.Store] = s.newEntry(list, 0)
			s.sets.Add(1)
		}
	}
	return out, nil
}

func (s *Store) sortSource(key string, now int64) ([]string, error) {
	e, ok := s.lookupRead(s.shardFor(key), key, now)
	if !ok {
		return nil, nil
	}
	switch v := e.value.(type) {
	case *listValue:
		items := v.toSlice()
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = string(item)
		}
		return out, nil
	case *setValue:
		return sortedSetKeys(v.members), nil
	case *zsetValue:
		entries := v.list.all()
		out := make([]string, len(entries))
		for i, entry := range entries {
			out[i] = entry.Member
		}
		return out, nil
	default:
		return nil, ErrWrongType
	}
}

// lookupPattern implements SORT's BY/GET pattern substitution, including the
// "->field" hash dereference. It returns false when the pattern has no '*' or
// the referenced value is missing or of the wrong type.
func (s *Store) lookupPattern(pattern, substitution string, now int64) ([]byte, bool) {
	if pattern == "#" {
		return []byte(substitution), true
	}
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return nil, false
	}

	rest := pattern[star+1:]
	field := ""
	if arrow := strings.Index(rest, "->"); arrow >= 0 && arrow+2 < len(rest) {
		field = rest[arrow+2:]
		rest = rest[:arrow]
	}
	refKey := pattern[:star] + substitution + rest

	e, ok := s.lookupRead(s.shardFor(refKey), refKey, now)
	if !ok {
		return nil, false
	}
	if field != "" {
		hv, ok := e.value.(*hashValue)
		if !ok {
			return nil, false
		}
		value, ok := hv.fields[field]
		if !ok {
			return nil, false
		}
		return value, true
	}
	sv, ok := e.value.(*stringValue)
	if !ok {
		return nil, false
	}
	return sv.data, true
}
