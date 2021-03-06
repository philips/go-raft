package raft

import (
	"errors"
	"sync"
	"time"
)

//------------------------------------------------------------------------------
//
// Typedefs
//
//------------------------------------------------------------------------------

// A peer is a reference to another server involved in the consensus protocol.
type Peer struct {
	server         *Server
	name           string
	prevLogIndex   uint64
	mutex          sync.Mutex
	heartbeatTimer *Timer
}

//------------------------------------------------------------------------------
//
// Constructor
//
//------------------------------------------------------------------------------

// Creates a new peer.
func NewPeer(server *Server, name string, heartbeatTimeout time.Duration) *Peer {
	p := &Peer{
		server:         server,
		name:           name,
		heartbeatTimer: NewTimer(heartbeatTimeout, heartbeatTimeout),
	}

	// Start the heartbeat timeout.
	go p.heartbeatTimeoutFunc()

	return p
}

//------------------------------------------------------------------------------
//
// Accessors
//
//------------------------------------------------------------------------------

// Retrieves the name of the peer.
func (p *Peer) Name() string {
	return p.name
}

// Retrieves the heartbeat timeout.
func (p *Peer) HeartbeatTimeout() time.Duration {
	return p.heartbeatTimer.MinDuration()
}

// Sets the heartbeat timeout.
func (p *Peer) SetHeartbeatTimeout(duration time.Duration) {
	p.heartbeatTimer.SetDuration(duration)
}

//------------------------------------------------------------------------------
//
// Methods
//
//------------------------------------------------------------------------------

//--------------------------------------
// State
//--------------------------------------

// Resumes the peer heartbeating.
func (p *Peer) resume() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.heartbeatTimer.Reset()
}

// Pauses the peer to prevent heartbeating.
func (p *Peer) pause() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.heartbeatTimer.Pause()
}

// Stops the peer entirely.
func (p *Peer) stop() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.heartbeatTimer.Stop()
}

//--------------------------------------
// Flush
//--------------------------------------

// Sends an AppendEntries RPC but does not obtain a lock on the server. This
// method should only be called from the server.
func (p *Peer) internalFlush() (uint64, bool, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	req := p.server.createInternalAppendEntriesRequest(p.prevLogIndex)
	return p.sendFlushRequest(req)
}

// Flushes a request through the server's transport.
func (p *Peer) sendFlushRequest(req *AppendEntriesRequest) (uint64, bool, error) {
	// Ignore any null requests.
	if req == nil {
		return 0, false, errors.New("raft.Peer: Request required")
	}

	// Generate an AppendEntries request based on the state of the server and
	// log. Send the request through the user-provided handler and process the
	// result.
	resp, err := p.server.transporter.SendAppendEntriesRequest(p.server, p, req)
	p.heartbeatTimer.Reset()
	if resp == nil {
		return 0, false, err
	}

	// If successful then update the previous log index. If it was
	// unsuccessful then decrement the previous log index and we'll try again
	// next time.
	if resp.Success {
		if len(req.Entries) > 0 {
			p.prevLogIndex = req.Entries[len(req.Entries)-1].Index
		}
	} else {
		// Decrement the previous log index down until we find a match. Don't
		// let it go below where the peer's commit index is though. That's a
		// problem.
		if p.prevLogIndex > 0 {
			p.prevLogIndex--
		}
		if resp.CommitIndex > p.prevLogIndex {
			p.prevLogIndex = resp.CommitIndex
		}
	}

	return resp.Term, resp.Success, err
}

//--------------------------------------
// Heartbeat
//--------------------------------------

// Listens to the heartbeat timeout and flushes an AppendEntries RPC.
func (p *Peer) heartbeatTimeoutFunc() {
	for {
		// Grab the current timer channel.
		p.mutex.Lock()
		var c chan time.Time
		if p.heartbeatTimer != nil {
			c = p.heartbeatTimer.C()
		}
		p.mutex.Unlock()

		// If the channel or timer are gone then exit.
		if c == nil {
			break
		}

		// Flush the peer when we get a heartbeat timeout. If the channel is
		// closed then the peer is getting cleaned up and we should exit.
		if _, ok := <-c; ok {
			// Retrieve the peer data within a lock that is separate from the
			// server lock when creating the request. Otherwise a deadlock can
			// occur.
			p.mutex.Lock()
			server, prevLogIndex := p.server, p.prevLogIndex
			p.mutex.Unlock()

			// Lock the server to create a request.
			req := server.createAppendEntriesRequest(prevLogIndex)

			p.mutex.Lock()
			p.sendFlushRequest(req)
			p.mutex.Unlock()
		} else {
			break
		}
	}
}
