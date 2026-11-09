package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

// SyncPolicy controls how often the append-only file is fsynced.
type SyncPolicy string

const (
	// SyncAlways fsyncs before a write command is acknowledged.
	SyncAlways SyncPolicy = "always"
	// SyncEverySec fsyncs once per second, losing at most one second on crash.
	SyncEverySec SyncPolicy = "everysec"
	// SyncNo relies on the operating system to flush the file.
	SyncNo SyncPolicy = "no"
)

// ParseSyncPolicy validates an appendfsync setting.
func ParseSyncPolicy(s string) (SyncPolicy, error) {
	switch SyncPolicy(s) {
	case SyncAlways, SyncEverySec, SyncNo:
		return SyncPolicy(s), nil
	}
	return "", fmt.Errorf("invalid appendfsync policy %q", s)
}

// ErrAOFClosed is returned when appending after Close.
var ErrAOFClosed = errors.New("append-only file is closed")

// AOF is an append-only command log stored as RESP arrays.
type AOF struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	policy SyncPolicy
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

// OpenAOF opens (creating if needed) the log at path.
func OpenAOF(path string, policy SyncPolicy) (*AOF, error) {
	if _, err := ParseSyncPolicy(string(policy)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	aof := &AOF{
		path:   path,
		file:   file,
		policy: policy,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go aof.syncLoop()
	return aof, nil
}

// Path returns the log location.
func (a *AOF) Path() string { return a.path }

// Policy returns the current fsync policy.
func (a *AOF) Policy() SyncPolicy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

// SetPolicy changes the fsync policy at runtime.
func (a *AOF) SetPolicy(policy SyncPolicy) error {
	if _, err := ParseSyncPolicy(string(policy)); err != nil {
		return err
	}
	a.mu.Lock()
	a.policy = policy
	a.mu.Unlock()
	return nil
}

// Size returns the current log size in bytes.
func (a *AOF) Size() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return 0
	}
	info, err := a.file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func (a *AOF) syncLoop() {
	defer close(a.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.mu.Lock()
			if !a.closed && a.file != nil && a.policy == SyncEverySec {
				_ = a.file.Sync()
			}
			a.mu.Unlock()
		case <-a.stop:
			return
		}
	}
}

// Sync flushes the file to stable storage.
func (a *AOF) Sync() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return ErrAOFClosed
	}
	return a.file.Sync()
}

// Append durably records one command.
func (a *AOF) Append(args [][]byte) error {
	buf := resp.AppendCommand(nil, args)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.file == nil {
		return ErrAOFClosed
	}
	if _, err := a.file.Write(buf); err != nil {
		return err
	}
	if a.policy == SyncAlways {
		return a.file.Sync()
	}
	return nil
}

// Close stops background syncing and closes the file.
func (a *AOF) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	close(a.stop)
	a.mu.Unlock()

	// Wait for the syncer to exit; it may be holding the lock.
	<-a.done

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Sync()
	if cerr := a.file.Close(); err == nil {
		err = cerr
	}
	a.file = nil
	return err
}

// Load replays every command in the log. A partially written trailing command
// (the normal outcome of a crash) is truncated away and reported.
func (a *AOF) Load(replay func(args [][]byte) error) (truncatedBytes int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return 0, ErrAOFClosed
	}
	if _, err := a.file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	counter := &countingReader{r: a.file}
	reader := resp.NewReader(counter)

	var lastGood int64
	for {
		args, readErr := reader.ReadCommand()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if terr := a.file.Truncate(lastGood); terr != nil {
				return 0, fmt.Errorf("truncate partial command at %d: %w", lastGood, terr)
			}
			if _, serr := a.file.Seek(0, io.SeekEnd); serr != nil {
				return 0, serr
			}
			return lastGood, nil
		}
		if err := replay(args); err != nil {
			return 0, err
		}
		lastGood = counter.n - int64(reader.Buffered())
	}

	if _, err := a.file.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	return 0, nil
}

// Rewrite replaces the log with the output of write, atomically.
//
// write is called with a writer attached to a temporary file; the temporary
// file replaces the log only after it has been fsynced.
func (a *AOF) Rewrite(write func(w *resp.Writer) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.file == nil {
		return ErrAOFClosed
	}

	tmp, err := os.CreateTemp(filepath.Dir(a.path), "redlite-aof-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	writer := resp.NewWriter(tmp)
	if err := write(writer); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := writer.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, a.path); err != nil {
		return err
	}

	reopened, err := os.OpenFile(a.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	old := a.file
	a.file = reopened
	return old.Close()
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
