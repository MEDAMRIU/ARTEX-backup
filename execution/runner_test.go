package execution

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type countingPipe struct{ writes atomic.Int32 }

func (p *countingPipe) Write(data []byte) (int, error) { p.writes.Add(1); return len(data), nil }
func (*countingPipe) Close() error                     { return nil }

var _ io.WriteCloser = (*countingPipe)(nil)

func TestCancelledQueuedCallNeverWritesOrKillsSession(t *testing.T) {
	pipe := &countingPipe{}
	s := &sandboxSession{stdin: pipe, done: make(chan struct{}), lastUsed: time.Now()}
	r := &Runner{sessions: map[string]*sandboxSession{".": s}}
	s.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan Reply, 1)
	go func() { finished <- r.call(ctx, Call{Name: "Bash", Directory: "."}) }()
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		queued := s.users == 1
		r.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			s.mu.Unlock()
			t.Fatal("call did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	s.mu.Unlock()
	select {
	case reply := <-finished:
		if reply.Error == "" {
			t.Fatal("cancelled call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled call did not return")
	}
	if pipe.writes.Load() != 0 || s.closed || s.users != 0 {
		t.Fatal("cancelled call wrote to or killed the session, or leaked its reference")
	}
}
