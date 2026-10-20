package engineproc

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LogBytes bounds each engine log file; one earlier file is kept.
const LogBytes = 8 << 20

// Log keeps an engine's output in a file of the provider's private state, at
// most max bytes, and moves a full file to path.1, replacing the one before.
// Engine output carries prompts and request bodies; it is never forwarded to
// a caller (KIT-P2).
type Log struct {
	mu   sync.Mutex
	path string
	max  int64
	file *os.File
	size int64
}

// OpenLog opens the log at the absolute path, bounded at max bytes.
func OpenLog(path string, max int64) (*Log, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("the engine log %s is not absolute", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &Log{path: path, max: max}
	return l, l.open()
}

func (l *Log) open() error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		//unchecked: the stat failure is the error returned
		file.Close()
		return err
	}
	l.file, l.size = file, info.Size()
	return nil
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return len(p), nil
	}
	if l.size+int64(len(p)) > l.max {
		//unchecked: the rotation reopens below; a failed close of the full file loses nothing written
		l.file.Close()
		//unchecked: an absent or locked previous file leaves the rename below to report
		os.Remove(l.path + ".1")
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			//unchecked: a log that cannot rotate is truncated instead, keeping the bound
			os.Truncate(l.path, 0)
		}
		if err := l.open(); err != nil {
			l.file = nil
			return len(p), nil
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	if err != nil {
		// A log that cannot be written stops keeping output; the engine's
		// pipe keeps draining so it never blocks on a full buffer.
		//unchecked: the write failure already ended this log
		l.file.Close()
		l.file = nil
	}
	return len(p), nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
