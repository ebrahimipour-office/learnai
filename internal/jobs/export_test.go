package jobs

// SetFinalWriteFault makes each final status write attempt fail while f returns an error (CR-38).
func (r *Runner) SetFinalWriteFault(f func() error) { r.finalWriteFault = f }

// SetReplayFault makes Recover's journal replay fail while f returns an error (T13.7).
func (r *Runner) SetReplayFault(f func() error) { r.replayFault = f }
