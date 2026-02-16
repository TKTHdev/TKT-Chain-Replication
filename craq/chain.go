package main

import (
	"log"
	"net"
	"sync"
)

type Version struct {
	Num   uint64
	Value string
	Clean bool
}

type VersionList struct {
	Versions []Version
}

// Latest returns the most recent version, or nil if empty.
func (vl *VersionList) Latest() *Version {
	if len(vl.Versions) == 0 {
		return nil
	}
	return &vl.Versions[len(vl.Versions)-1]
}

// LatestClean returns the most recent clean version, or nil if none.
func (vl *VersionList) LatestClean() *Version {
	for i := len(vl.Versions) - 1; i >= 0; i-- {
		if vl.Versions[i].Clean {
			return &vl.Versions[i]
		}
	}
	return nil
}

// FindVersion returns the version with the given number, or nil.
func (vl *VersionList) FindVersion(num uint64) *Version {
	for i := range vl.Versions {
		if vl.Versions[i].Num == num {
			return &vl.Versions[i]
		}
	}
	return nil
}

// Append adds a new version to the list.
func (vl *VersionList) Append(v Version) {
	vl.Versions = append(vl.Versions, v)
}

// MarkClean marks the specified version as clean and GCs older versions.
func (vl *VersionList) MarkClean(num uint64) {
	idx := -1
	for i := range vl.Versions {
		if vl.Versions[i].Num == num {
			vl.Versions[i].Clean = true
			idx = i
			break
		}
	}
	if idx > 0 {
		// GC all versions older than the newly cleaned one
		vl.Versions = vl.Versions[idx:]
	}
}

type ChainNode struct {
	// Node identity
	me    int
	peers map[int]string // id -> "ip:port"

	// Chain structure
	predecessor int // upstream node ID (-1 if head)
	successor   int // downstream node ID (-1 if tail)
	isHead      bool
	isTail      bool
	tailID      int // tail node ID (for version queries)

	// UDP connection
	udpConn *net.UDPConn

	// State machine (multi-version KV store)
	store      map[string]*VersionList
	versionSeq uint64 // head increments per write

	// Pending version queries (queryID -> channel returning committed version num)
	pendingQueries map[uint64]chan uint64
	pendingQueryMu sync.Mutex
	nextQuerySeq   uint64

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

	tailID := ids[len(ids)-1]

	node := &ChainNode{
		me:             id,
		peers:          peers,
		predecessor:    predecessor,
		successor:      successor,
		isHead:         pos == 0,
		isTail:         pos == len(ids)-1,
		tailID:         tailID,
		store:          make(map[string]*VersionList),
		pendingQueries: make(map[uint64]chan uint64),
		writeCh:        make(chan ClientRequest, 1000),
		readCh:         make(chan ClientRequest, 1000),
		putCh:          make(chan *Message, 1000),
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
