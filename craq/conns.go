package main

import (
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

func (c *ChainNode) listen() error {
	addr, err := net.ResolveUDPAddr("udp", c.peers[c.me])
	if err != nil {
		log.Printf("[Node %d] Failed to resolve address: %v", c.me, err)
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("[Node %d] Failed to listen UDP: %v", c.me, err)
		return err
	}
	c.udpConn = conn
	c.log("Listening on %s (UDP)", c.peers[c.me])

	buf := make([]byte, 65535)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			c.log("Failed to read UDP: %v", err)
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		go c.handleMessage(data, remoteAddr)
	}
}

func (c *ChainNode) handleMessage(data []byte, from *net.UDPAddr) {
	if len(data) == 0 {
		return
	}

	if data[0] == MsgTypeChainForward {
		c.handleChainForward(data)
		return
	}

	msg, err := DecodeMessage(data)
	if err != nil {
		c.log("Failed to decode message: %v", err)
		return
	}

	switch msg.Type {
	case MsgTypePut:
		c.handlePut(msg, from)
	case MsgTypeGet:
		c.handleGet(msg, from)
	case MsgTypeChainAck:
		c.handleChainAck(msg)
	case MsgTypeVersionQuery:
		c.handleVersionQuery(msg, from)
	case MsgTypeVersionResponse:
		c.handleVersionResponse(msg)
	}
}

func (c *ChainNode) handlePut(msg *Message, from *net.UDPAddr) {
	if c.isHead {
		if msg.ClientAddr == "" {
			msg.ClientAddr = from.String()
		}
		c.putCh <- msg
		return
	}

	c.log("PUT key=%s value=%s seq=%d (non-head, should not happen)", msg.Key, msg.Value, msg.Seq)
}

func (c *ChainNode) handleGet(msg *Message, from *net.UDPAddr) {
	c.log("GET key=%s seq=%d", msg.Key, msg.Seq)

	c.mu.RLock()
	vl, exists := c.store[msg.Key]
	if !exists {
		c.mu.RUnlock()
		// Key doesn't exist: respond with empty value
		resp := &Message{
			Type: MsgTypeResponse,
			Seq:  msg.Seq,
			Key:  msg.Key,
		}
		c.sendToAddr(from, resp.Encode())
		return
	}

	latest := vl.Latest()
	if latest == nil {
		c.mu.RUnlock()
		resp := &Message{
			Type: MsgTypeResponse,
			Seq:  msg.Seq,
			Key:  msg.Key,
		}
		c.sendToAddr(from, resp.Encode())
		return
	}

	// Fast path: tail always has the authoritative version, or latest is clean
	if c.isTail || latest.Clean {
		resp := &Message{
			Type:  MsgTypeResponse,
			Seq:   msg.Seq,
			Key:   msg.Key,
			Value: latest.Value,
		}
		c.mu.RUnlock()
		c.sendToAddr(from, resp.Encode())
		return
	}
	c.mu.RUnlock()

	// Slow path: latest is dirty, query tail for committed version
	committedVer, ok := c.queryTailForVersion(msg.Key)
	if !ok {
		// Timeout or error: respond with empty value
		resp := &Message{
			Type: MsgTypeResponse,
			Seq:  msg.Seq,
			Key:  msg.Key,
		}
		c.sendToAddr(from, resp.Encode())
		return
	}

	c.mu.RLock()
	vl, exists = c.store[msg.Key]
	value := ""
	if exists {
		v := vl.FindVersion(committedVer)
		if v != nil {
			value = v.Value
		} else {
			// Version was GC'd, use latest clean
			lc := vl.LatestClean()
			if lc != nil {
				value = lc.Value
			}
		}
	}
	c.mu.RUnlock()

	resp := &Message{
		Type:  MsgTypeResponse,
		Seq:   msg.Seq,
		Key:   msg.Key,
		Value: value,
	}
	c.sendToAddr(from, resp.Encode())
}

// queryTailForVersion sends a VersionQuery to the tail and waits for response.
func (c *ChainNode) queryTailForVersion(key string) (uint64, bool) {
	queryID := atomic.AddUint64(&c.nextQuerySeq, 1)

	ch := make(chan uint64, 1)
	c.pendingQueryMu.Lock()
	c.pendingQueries[queryID] = ch
	c.pendingQueryMu.Unlock()

	defer func() {
		c.pendingQueryMu.Lock()
		delete(c.pendingQueries, queryID)
		c.pendingQueryMu.Unlock()
	}()

	msg := &Message{
		Type:    MsgTypeVersionQuery,
		Key:     key,
		QueryID: queryID,
	}
	c.sendTo(c.tailID, msg.Encode())

	select {
	case ver := <-ch:
		return ver, true
	case <-time.After(5 * time.Second):
		return 0, false
	}
}

// handleVersionQuery runs on the tail: responds with the latest version number for the key.
func (c *ChainNode) handleVersionQuery(msg *Message, from *net.UDPAddr) {
	c.mu.RLock()
	var ver uint64
	vl, exists := c.store[msg.Key]
	if exists {
		latest := vl.Latest()
		if latest != nil {
			ver = latest.Num
		}
	}
	c.mu.RUnlock()

	resp := &Message{
		Type:    MsgTypeVersionResponse,
		Key:     msg.Key,
		Version: ver,
		QueryID: msg.QueryID,
	}
	c.sendToAddr(from, resp.Encode())
}

// handleVersionResponse delivers the version response to the waiting query.
func (c *ChainNode) handleVersionResponse(msg *Message) {
	c.pendingQueryMu.Lock()
	ch, ok := c.pendingQueries[msg.QueryID]
	c.pendingQueryMu.Unlock()

	if ok {
		ch <- msg.Version
	}
}

func (c *ChainNode) sendAckToClient(msg *Message) {
	ack := &Message{
		Type: MsgTypeAck,
		Seq:  msg.Seq,
		Key:  msg.Key,
	}

	clientAddr, err := net.ResolveUDPAddr("udp", msg.ClientAddr)
	if err != nil {
		c.log("Failed to resolve client address: %v", err)
		return
	}

	c.sendToAddr(clientAddr, ack.Encode())
}

// sendChainAck sends a ChainAck message to the predecessor, propagating backwards.
func (c *ChainNode) sendChainAck(key string, version uint64) {
	if c.predecessor == -1 {
		return
	}
	msg := &Message{
		Type:    MsgTypeChainAck,
		Key:     key,
		Version: version,
	}
	c.sendToPredecessor(msg.Encode())
}

// handleChainAck processes a ChainAck: marks version clean and propagates to predecessor.
func (c *ChainNode) handleChainAck(msg *Message) {
	c.log("ChainAck key=%s ver=%d", msg.Key, msg.Version)
	c.commitVersion(msg.Key, msg.Version)

	// Propagate to predecessor (unless we are head)
	if !c.isHead {
		c.sendChainAck(msg.Key, msg.Version)
	}
}

// commitVersion marks a version as clean and GCs older versions.
func (c *ChainNode) commitVersion(key string, ver uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vl, exists := c.store[key]
	if !exists {
		return
	}
	vl.MarkClean(ver)
}

func (c *ChainNode) sendToAddr(addr *net.UDPAddr, data []byte) error {
	_, err := c.udpConn.WriteToUDP(data, addr)
	if err != nil {
		c.log("Failed to send to %s: %v", addr, err)
		return err
	}
	return nil
}

func (c *ChainNode) sendTo(peerID int, data []byte) error {
	addr, err := net.ResolveUDPAddr("udp", c.peers[peerID])
	if err != nil {
		return err
	}

	_, err = c.udpConn.WriteToUDP(data, addr)
	if err != nil {
		c.log("Failed to send to peer %d: %v", peerID, err)
		return err
	}
	return nil
}

func (c *ChainNode) sendToSuccessor(data []byte) error {
	if c.successor == -1 {
		return nil
	}
	return c.sendTo(c.successor, data)
}

func (c *ChainNode) sendToPredecessor(data []byte) error {
	if c.predecessor == -1 {
		return nil
	}
	return c.sendTo(c.predecessor, data)
}

// putHandler runs on the head node. It processes writes serially,
// assigning version numbers and chain sequence numbers.
func (c *ChainNode) putHandler() {
	for msg := range c.putCh {
		c.mu.Lock()
		c.versionSeq++
		ver := c.versionSeq
		c.mu.Unlock()

		msg.Version = ver
		c.log("PUT key=%s value=%s seq=%d ver=%d", msg.Key, msg.Value, msg.Seq, ver)

		// Write as dirty version
		c.mu.Lock()
		vl, exists := c.store[msg.Key]
		if !exists {
			vl = &VersionList{}
			c.store[msg.Key] = vl
		}
		vl.Append(Version{Num: ver, Value: msg.Value, Clean: false})
		c.mu.Unlock()

		if c.isTail {
			// Single node: commit immediately
			c.commitVersion(msg.Key, ver)
			c.sendAckToClient(msg)
		} else {
			c.chainSeq++
			c.sendToSuccessor(EncodeChainForward(c.chainSeq, msg.Encode()))
		}
	}
}

// handleChainForward receives a chain-forwarded message, reorders by
// sequence number, and processes in the head's original order.
func (c *ChainNode) handleChainForward(data []byte) {
	seq, _ := DecodeChainForward(data)

	c.chainMu.Lock()
	defer c.chainMu.Unlock()

	if seq < c.nextChainSeq {
		return // duplicate
	}
	if seq > c.nextChainSeq {
		c.chainBuf[seq] = data
		return
	}

	// seq == nextChainSeq: process this and drain buffer
	c.processChainPayload(data)
	c.nextChainSeq++

	for {
		next, ok := c.chainBuf[c.nextChainSeq]
		if !ok {
			break
		}
		c.processChainPayload(next)
		delete(c.chainBuf, c.nextChainSeq)
		c.nextChainSeq++
	}
}

// processChainPayload applies the inner message to the version store and forwards.
func (c *ChainNode) processChainPayload(wrapped []byte) {
	_, payload := DecodeChainForward(wrapped)

	msg, err := DecodeMessage(payload)
	if err != nil {
		c.log("Failed to decode message in chain forward: %v", err)
		return
	}
	c.log("PUT key=%s value=%s seq=%d ver=%d (chain-fwd)", msg.Key, msg.Value, msg.Seq, msg.Version)

	// Write as dirty version
	c.mu.Lock()
	vl, exists := c.store[msg.Key]
	if !exists {
		vl = &VersionList{}
		c.store[msg.Key] = vl
	}
	vl.Append(Version{Num: msg.Version, Value: msg.Value, Clean: false})
	c.mu.Unlock()

	if c.isTail {
		// Tail: commit, ACK client, and start chain ACK backpropagation
		c.commitVersion(msg.Key, msg.Version)
		c.sendAckToClient(msg)
		c.sendChainAck(msg.Key, msg.Version)
	} else {
		c.sendToSuccessor(wrapped)
	}
}

func (c *ChainNode) log(format string, args ...interface{}) {
	if c.debug {
		msg := fmt.Sprintf(format, args...)
		log.Printf("[Node %d] %s", c.me, msg)
	}
}
