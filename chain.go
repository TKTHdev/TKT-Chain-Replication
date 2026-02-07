package main

import (
	"log"
	"net"
	"sync"
)

type ChainNode struct {
	// Node identity
	me    int
	peers map[int]string // id -> "ip:port"

	// Chain structure
	predecessor int // upstream node ID (-1 if head)
	successor   int // downstream node ID (-1 if tail)
	isHead      bool
	isTail      bool

	// UDP connection
	udpConn *net.UDPConn

	// State machine (KV store)
	state map[string]string

	// Pending write requests (seq -> response channel)
	pendingWrites map[uint64]chan Response
	nextSeq       uint64 // next sequence number

	// Client request channels
	writeCh chan ClientRequest
	readCh  chan ClientRequest

	// Head write queue
	putCh chan *Message

	// Chain FIFO ordering
	chainSeq     uint64            // outbound counter (head only)
	nextChainSeq uint64            // next expected inbound seq (non-head)
	chainBuf     map[uint64][]byte // reorder buffer for early arrivals
	chainMu      sync.Mutex        // serializes chain message processing

	// Synchronization
	mu sync.RWMutex

	// Configuration
	debug bool
}

type ClientRequest struct {
	Op     string // "GET" or "PUT"
	Key    string
	Value  string
	RespCh chan Response
}

type Response struct {
	Success bool
	Value   string
	Err     string
}

func NewChainNode(id int, confPath string, debug bool) *ChainNode {
	peers := parseConfig(confPath)

	// Determine chain order (ascending by ID)
	ids := sortedIDs(peers)
	pos := indexOf(ids, id)

	predecessor := -1
	successor := -1
	if pos > 0 {
		predecessor = ids[pos-1]
	}
	if pos < len(ids)-1 {
		successor = ids[pos+1]
	}

	node := &ChainNode{
		me:             id,
		peers:          peers,
		predecessor:    predecessor,
		successor:      successor,
		isHead:         pos == 0,
		isTail:         pos == len(ids)-1,
		state:          make(map[string]string),
		pendingWrites:  make(map[uint64]chan Response),
		nextSeq:        0,
		writeCh: make(chan ClientRequest, 1000),
		readCh:  make(chan ClientRequest, 1000),
		putCh:   make(chan *Message, 1000),
		chainBuf:       make(map[uint64][]byte),
		nextChainSeq:   1,
		debug:          debug,
	}

	return node
}

func (c *ChainNode) Run() {
	role := "middle"
	if c.isHead {
		role = "head"
	} else if c.isTail {
		role = "tail"
	}
	log.Printf("[Node %d] Starting as %s on %s", c.me, role, c.peers[c.me])

	go c.listen()

	if c.isHead {
		go c.putHandler()
	}

	select {}
}
