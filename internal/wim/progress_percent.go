package wim

// percentProgress turns WIMGAPI's whole-percent split progress into the
// monotonic (done, total) byte counts a ProgressFunc receives, measured
// against the source WIM's size.
type percentProgress struct {
	total, last uint64
	reported    bool
	report      func(done, total uint64)
}

func (p *percentProgress) percent(pct uint64) {
	pct = min(pct, 100)
	p.emit(p.total/100*pct + p.total%100*pct/100)
}

// finish reports the split complete; WIMGAPI need not send 100%.
func (p *percentProgress) finish() { p.emit(p.total) }

func (p *percentProgress) emit(done uint64) {
	if p.total == 0 || (p.reported && done <= p.last) {
		return
	}
	p.reported, p.last = true, done
	p.report(done, p.total)
}
