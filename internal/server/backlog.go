package server

import "sync"

// replBacklog is a bounded byte buffer of recent replication stream data. The
// offset of a byte is its position in the stream, starting at the first
// propagated command.
type replBacklog struct {
	mu    sync.Mutex
	buf   []byte
	start int64
	limit int
}

func newReplBacklog(limit int64) *replBacklog {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &replBacklog{limit: int(limit)}
}

func (b *replBacklog) append(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, data...)
	if len(b.buf) > b.limit {
		drop := len(b.buf) - b.limit
		b.buf = append([]byte(nil), b.buf[drop:]...)
		b.start += int64(drop)
	}
}

// end returns the offset just past the last buffered byte.
func (b *replBacklog) end() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.start + int64(len(b.buf))
}

// since returns the bytes from offset to the end, when offset is still covered.
func (b *replBacklog) since(offset int64) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	end := b.start + int64(len(b.buf))
	if offset < b.start || offset > end {
		return nil, false
	}
	return append([]byte(nil), b.buf[offset-b.start:]...), true
}
