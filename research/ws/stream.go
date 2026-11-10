package ws

// Latest is a one-slot mailbox: Put never blocks and replaces any unsent
// value, so a slow client skips video frames instead of buffering them.
// Single producer, single consumer.
type Latest chan []byte

// NewLatest returns an empty mailbox.
func NewLatest() Latest { return make(Latest, 1) }

// Put stores b, discarding an unsent older value. Reports whether one was dropped.
func (l Latest) Put(b []byte) (dropped bool) {
	select {
	case l <- b:
	default:
		select {
		case <-l:
			dropped = true
		default:
		}
		l <- b // single producer: slot is free now
	}
	return
}

// Stream sends video (latest-wins) and audio (bounded, drop on full) on c
// until done is closed or a write fails. Write stalls are bounded by
// c.WriteTimeout; after a failed write the connection is closed.
func Stream(c *Conn, video Latest, audio <-chan []byte, done <-chan struct{}) (err error) {
	for err == nil {
		select {
		case <-done:
			return c.Close()
		case f := <-video:
			err = c.WriteMessage(OpBinary, f)
		case a := <-audio:
			err = c.WriteMessage(OpBinary, a)
		}
	}
	_ = c.nc.Close()
	return
}
