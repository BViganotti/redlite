package store

// ValueType identifies the type of a stored value. It is the source of truth
// for TYPE and for WRONGTYPE errors.
type ValueType uint8

const (
	TypeString ValueType = iota + 1
	TypeHash
	TypeList
	TypeSet
	TypeZSet
)

// String returns the name Redis uses for the type.
func (t ValueType) String() string {
	switch t {
	case TypeString:
		return "string"
	case TypeHash:
		return "hash"
	case TypeList:
		return "list"
	case TypeSet:
		return "set"
	case TypeZSet:
		return "zset"
	default:
		return "none"
	}
}

// value is the sealed interface implemented by every stored type.
type value interface {
	kind() ValueType
	// clone returns an independent copy, so a duplicated key cannot share
	// mutable state with its source.
	clone() value
}

type stringValue struct {
	data []byte
}

func (*stringValue) kind() ValueType { return TypeString }

func (v *stringValue) clone() value {
	return &stringValue{data: append([]byte(nil), v.data...)}
}

type hashValue struct {
	fields map[string][]byte
}

func (*hashValue) kind() ValueType { return TypeHash }

func (v *hashValue) clone() value {
	c := &hashValue{fields: make(map[string][]byte, len(v.fields))}
	for field, data := range v.fields {
		c.fields[field] = append([]byte(nil), data...)
	}
	return c
}

type setValue struct {
	members map[string]struct{}
}

func (*setValue) kind() ValueType { return TypeSet }

func (v *setValue) clone() value {
	c := &setValue{members: make(map[string]struct{}, len(v.members))}
	for member := range v.members {
		c.members[member] = struct{}{}
	}
	return c
}

// listValue is a growable ring buffer. Push and pop at both ends are O(1)
// amortised, which is the property the list commands depend on.
type listValue struct {
	buf  [][]byte
	head int
	size int
}

func (*listValue) kind() ValueType { return TypeList }

func newListValue() *listValue {
	return &listValue{buf: make([][]byte, 8)}
}

func (v *listValue) clone() value {
	c := newListValue()
	for i := 0; i < v.size; i++ {
		c.pushBack(append([]byte(nil), v.at(i)...))
	}
	return c
}

func (l *listValue) len() int { return l.size }

func (l *listValue) grow() {
	if l.size < len(l.buf) {
		return
	}
	next := make([][]byte, len(l.buf)*2)
	for i := 0; i < l.size; i++ {
		next[i] = l.at(i)
	}
	l.buf = next
	l.head = 0
}

func (l *listValue) pushFront(v []byte) {
	l.grow()
	l.head = (l.head - 1 + len(l.buf)) % len(l.buf)
	l.buf[l.head] = v
	l.size++
}

func (l *listValue) pushBack(v []byte) {
	l.grow()
	l.buf[(l.head+l.size)%len(l.buf)] = v
	l.size++
}

func (l *listValue) popFront() ([]byte, bool) {
	if l.size == 0 {
		return nil, false
	}
	v := l.buf[l.head]
	l.buf[l.head] = nil
	l.head = (l.head + 1) % len(l.buf)
	l.size--
	return v, true
}

func (l *listValue) popBack() ([]byte, bool) {
	if l.size == 0 {
		return nil, false
	}
	idx := (l.head + l.size - 1) % len(l.buf)
	v := l.buf[idx]
	l.buf[idx] = nil
	l.size--
	return v, true
}

func (l *listValue) at(i int) []byte {
	return l.buf[(l.head+i)%len(l.buf)]
}

func (l *listValue) set(i int, v []byte) {
	l.buf[(l.head+i)%len(l.buf)] = v
}

func (l *listValue) toSlice() [][]byte {
	out := make([][]byte, l.size)
	for i := range out {
		out[i] = l.at(i)
	}
	return out
}

func (l *listValue) reset(items [][]byte) {
	size := 8
	if len(items) > size {
		size = len(items)
	}
	l.buf = make([][]byte, size)
	copy(l.buf, items)
	l.head = 0
	l.size = len(items)
}
