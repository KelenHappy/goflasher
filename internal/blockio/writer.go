package blockio

// Writer streams bytes sequentially from offset 0 of an Aligned device. Whole
// sectors are written as soon as they are complete; a trailing partial sector
// is held until Flush, which merges it into the device with read-modify-write.
// It is not safe for concurrent use.
type Writer struct {
	device  *Aligned
	offset  int64
	pending []byte
	err     error
}

// NewWriter returns a Writer positioned at the start of a.
func NewWriter(a *Aligned) *Writer {
	return &Writer{device: a, pending: make([]byte, 0, a.sector)}
}

// Write accepts all of p unless the device fails. After a failure every later
// call returns that failure, because the stream position is no longer known.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	rest, err := w.topUpPending(p)
	if err != nil {
		return 0, err
	}
	whole := len(rest) / cap(w.pending) * cap(w.pending)
	if whole > 0 {
		if err := w.commit(rest[:whole]); err != nil {
			return 0, err
		}
	}
	w.pending = append(w.pending, rest[whole:]...)
	return len(p), nil
}

// topUpPending moves the start of p into a held partial sector, writes the
// sector once it is complete, and returns the rest of p. Without a held
// sector it returns p unchanged.
func (w *Writer) topUpPending(p []byte) ([]byte, error) {
	if len(w.pending) == 0 {
		return p, nil
	}
	n := min(len(p), cap(w.pending)-len(w.pending))
	w.pending = append(w.pending, p[:n]...)
	if len(w.pending) < cap(w.pending) {
		return p[n:], nil
	}
	return p[n:], w.commitPending()
}

// Flush writes a held partial sector, preserving the device bytes after it.
func (w *Writer) Flush() error {
	if w.err != nil {
		return w.err
	}
	if len(w.pending) == 0 {
		return nil
	}
	return w.commitPending()
}

func (w *Writer) commitPending() error {
	if err := w.commit(w.pending); err != nil {
		return err
	}
	w.pending = w.pending[:0]
	return nil
}

func (w *Writer) commit(p []byte) error {
	n, err := w.device.WriteAt(p, w.offset)
	w.offset += int64(n)
	if err != nil {
		w.err = err
	}
	return err
}
