package jobs

// SetFinalWriteFault makes each final status write attempt fail while f returns an error (CR-38).
func (r *Runner) SetFinalWriteFault(f func() error) { r.finalWriteFault = f }
