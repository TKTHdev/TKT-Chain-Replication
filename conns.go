package main

import (
	"fmt"
	"log"
	"net"
	"time"
)

const WRITE_LINGER_TIME = 1 * time.Millisecond

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

	// Check first byte for batch type before full decode
	if data[0] == MsgTypeBatchPut {
		c.handleBatchPut(data)
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
	}
}

func (c *ChainNode) handlePut(msg *Message, from *net.UDPAddr) {
	// Head with batching: enqueue to putCh
	if c.isHead {
		if msg.ClientAddr == "" {
			msg.ClientAddr = from.String()
		}
		c.putCh <- putRequest{msg: msg, from: from}
		return
	}

	c.log("PUT key=%s value=%s seq=%d", msg.Key, msg.Value, msg.Seq)

	// Apply to state machine
	c.mu.Lock()
	c.state[msg.Key] = msg.Value
	c.mu.Unlock()

	if c.isTail {
		c.sendAckToClient(msg)
	} else {
		c.sendToSuccessor(msg.Encode())
	}
}

func (c *ChainNode) handleGet(msg *Message, from *net.UDPAddr) {
	if !c.isTail {
		c.log("GET received but not tail, ignoring")
		return
	}

	c.log("GET key=%s seq=%d", msg.Key, msg.Seq)

	c.mu.RLock()
	value, exists := c.state[msg.Key]
	c.mu.RUnlock()

	resp := &Message{
		Type:  MsgTypeResponse,
		Seq:   msg.Seq,
		Key:   msg.Key,
		Value: value,
	}
	if !exists {
		resp.Value = ""
	}

	c.sendToAddr(from, resp.Encode())
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

// batchPutHandler runs on the head node. It accumulates PUT requests
// and flushes them as a batch when the batch size is reached or the
// linger timer fires.
func (c *ChainNode) batchPutHandler() {
	batchSize := c.writeBatchSize

	var pending []*Message
	var timer *time.Timer
	var timerCh <-chan time.Time

	stopTimer := func(t *time.Timer) {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}

	flush := func() {
		if len(pending) == 0 {
			return
		}

		// Apply all to state machine
		c.mu.Lock()
		for _, m := range pending {
			c.state[m.Key] = m.Value
		}
		c.mu.Unlock()

		if c.isTail {
			// Head is also tail (single node): ACK each
			for _, m := range pending {
				c.sendAckToClient(m)
			}
		} else {
			// Encode and forward batch to successor with chain seq
			c.chainSeq++
			c.sendToSuccessor(EncodeChainForward(c.chainSeq, EncodeBatch(pending)))
		}

		for _, m := range pending {
			c.log("PUT key=%s value=%s seq=%d (batched)", m.Key, m.Value, m.Seq)
		}
		pending = nil
	}

	for {
		select {
		case req := <-c.putCh:
			pending = append(pending, req.msg)
			if len(pending) >= batchSize {
				flush()
				if timer != nil {
					stopTimer(timer)
					timer = nil
					timerCh = nil
				}
			} else if timer == nil {
				timer = time.NewTimer(WRITE_LINGER_TIME)
				timerCh = timer.C
			}
		case <-timerCh:
			flush()
			timer = nil
			timerCh = nil
		}
	}
}

// handleBatchPut is used by middle and tail nodes to process a batch message.
func (c *ChainNode) handleBatchPut(data []byte) {
	msgs, err := DecodeBatch(data)
	if err != nil {
		c.log("Failed to decode batch: %v", err)
		return
	}

	// Apply all to state machine
	c.mu.Lock()
	for _, m := range msgs {
		c.state[m.Key] = m.Value
	}
	c.mu.Unlock()

	if c.isTail {
		// Send individual ACKs back to each client
		for _, m := range msgs {
			c.log("PUT key=%s value=%s seq=%d (batch-tail)", m.Key, m.Value, m.Seq)
			c.sendAckToClient(m)
		}
	} else {
		// Forward entire batch to successor
		for _, m := range msgs {
			c.log("PUT key=%s value=%s seq=%d (batch-mid)", m.Key, m.Value, m.Seq)
		}
		c.sendToSuccessor(data)
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

// processChainPayload applies the inner message to state and forwards
// the original wrapped data (preserving the head's chain seq) to the successor.
func (c *ChainNode) processChainPayload(wrapped []byte) {
	_, payload := DecodeChainForward(wrapped)

	if payload[0] == MsgTypeBatchPut {
		msgs, err := DecodeBatch(payload)
		if err != nil {
			c.log("Failed to decode batch in chain forward: %v", err)
			return
		}
		c.mu.Lock()
		for _, m := range msgs {
			c.state[m.Key] = m.Value
		}
		c.mu.Unlock()

		if c.isTail {
			for _, m := range msgs {
				c.log("PUT key=%s value=%s seq=%d (chain-tail)", m.Key, m.Value, m.Seq)
				c.sendAckToClient(m)
			}
		} else {
			for _, m := range msgs {
				c.log("PUT key=%s value=%s seq=%d (chain-mid)", m.Key, m.Value, m.Seq)
			}
			c.sendToSuccessor(wrapped)
		}
	} else {
		msg, err := DecodeMessage(payload)
		if err != nil {
			c.log("Failed to decode message in chain forward: %v", err)
			return
		}
		c.log("PUT key=%s value=%s seq=%d (chain-fwd)", msg.Key, msg.Value, msg.Seq)
		c.mu.Lock()
		c.state[msg.Key] = msg.Value
		c.mu.Unlock()

		if c.isTail {
			c.sendAckToClient(msg)
		} else {
			c.sendToSuccessor(wrapped)
		}
	}
}

func (c *ChainNode) log(format string, args ...interface{}) {
	if c.debug {
		msg := fmt.Sprintf(format, args...)
		log.Printf("[Node %d] %s", c.me, msg)
	}
}
