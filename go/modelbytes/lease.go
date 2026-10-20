package modelbytes

import (
	"io"
	"os"
	"sync"
)

// Lease is one engine's reference to lent bytes (KIT-MB4): a pipe whose
// read end OA keeps and whose write end the engine holds. The receiver
// closes its end at release, and the engine's exit closes it too; OA then
// reads end-of-file and Done closes, on every platform with one mechanism.
type Lease struct {
	r, w *os.File
	done chan struct{}
	once sync.Once
}

// NewLease opens the pipe. Give Writer to the engine, by inheritance or by
// duplication into it, then call Lent.
func NewLease() (*Lease, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	l := &Lease{r: r, w: w, done: make(chan struct{})}
	go func() {
		//unchecked: the engine writes nothing; any end of the read is the lease's end
		io.Copy(io.Discard, r)
		r.Close()
		close(l.done)
	}()
	return l, nil
}

// Writer is the end the engine receives. It stays open in this process
// until Lent or Close.
func (l *Lease) Writer() *os.File { return l.w }

// Lent closes this process's write end once the engine holds its own copy:
// from then on only the engine keeps the lease open.
func (l *Lease) Lent() {
	l.once.Do(func() {
		//unchecked: this process's copy; the engine's is unaffected
		l.w.Close()
	})
}

// Close ends the lease from OA's side when the engine never received it.
func (l *Lease) Close() { l.Lent() }

// Done closes when no process holds the write end any more.
func (l *Lease) Done() <-chan struct{} { return l.done }
