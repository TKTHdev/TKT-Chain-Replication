package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

func (c *ChainNode) listen() error {
	listener, err := net.Listen("tcp", c.peers[c.me])
	if err != nil {
		log.Printf("[Node %d] Failed to listen TCP: %v", c.me, err)
		return err
	}
	c.listener = listener
	c.log("Listening on %s (TCP)", c.peers[c.me])

	// Accept connections in a loop
	for {
		conn, err := listener.Accept()
		if err != nil {
			c.log("Failed to accept connection: %v", err)
			continue
		}
		go c.handleConnection(conn)
	}
}

func (c *ChainNode) handleConnection(conn net.Conn) {
	defer conn.Close()
	c.log("New connection from %s", conn.RemoteAddr())

	for {
		// Read message length (4 bytes)
		var msgLen uint32
		if err := binary.Read(conn, binary.BigEndian, &msgLen); err != nil {
			if err != io.EOF {
				c.log("Failed to read message length: %v", err)
			}
			return
		}

		// Read message data
		data := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, data); err != nil {
			c.log("Failed to read message data: %v", err)
			return
		}

		go c.handleMessage(data, conn)
	}
}

func (c *ChainNode) handleMessage(data []byte, conn net.Conn) {
	if len(data) == 0 {
		return
	}

	if data[0] == MsgTypeChainForward {
		c.handleChainForward(data)
		return
	}

	if data[0] == MsgTypeChainAck {
		c.handleChainAck(data)
		return
	}

	msg, err := DecodeMessage(data)
	if err != nil {
		c.log("Failed to decode message: %v", err)
		return
	}

	switch msg.Type {
	case MsgTypePut:
		c.handlePut(msg, conn)
	case MsgTypeGet:
		c.handleGet(msg, conn)
	}
}

func (c *ChainNode) handlePut(msg *Message, conn net.Conn) {
	// Head with batching: enqueue to putCh
	if c.isHead {
		if msg.ClientAddr == "" {
			msg.ClientAddr = conn.RemoteAddr().String()
			// Store client connection for later response
			c.clientConn.Store(msg.ClientAddr, conn)
		}
		c.putCh <- msg
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

func (c *ChainNode) handleGet(msg *Message, conn net.Conn) {
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

	c.sendToConn(conn, resp.Encode())
}

func (c *ChainNode) sendAckToClient(msg *Message) {
	if c.isHead {
		// Head: send ACK directly to client
		ack := &Message{
			Type: MsgTypeAck,
			Seq:  msg.Seq,
			Key:  msg.Key,
		}

		// Get client connection from the stored map
		if connInterface, ok := c.clientConn.Load(msg.ClientAddr); ok {
			if conn, ok := connInterface.(net.Conn); ok {
				c.sendToConn(conn, ack.Encode())
				return
			}
		}

		c.log("Failed to find client connection for %s", msg.ClientAddr)
	} else {
		// Non-head: send ChainAck back to predecessor
		ack := &Message{
			Type:       MsgTypeChainAck,
			Seq:        msg.Seq,
			Key:        msg.Key,
			ClientAddr: msg.ClientAddr,
		}
		c.sendToPredecessor(ack.Encode())
	}
}

func (c *ChainNode) sendToConn(conn net.Conn, data []byte) error {
	// Protect writes to prevent message interleaving
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Write length prefix
	msgLen := uint32(len(data))
	if err := binary.Write(conn, binary.BigEndian, msgLen); err != nil {
		c.log("Failed to write message length: %v", err)
		return err
	}

	// Write message data
	if _, err := conn.Write(data); err != nil {
		c.log("Failed to write message data: %v", err)
		return err
	}

	return nil
}

func (c *ChainNode) connectToPeer(peerID int) (net.Conn, error) {
	c.connsMu.RLock()
	conn, exists := c.peerConns[peerID]
	c.connsMu.RUnlock()

	if exists && conn != nil {
		return conn, nil
	}

	// Need to establish new connection
	c.connsMu.Lock()
	defer c.connsMu.Unlock()

	// Double-check after acquiring write lock
	if conn, exists := c.peerConns[peerID]; exists && conn != nil {
		return conn, nil
	}

	// Establish connection with retries
	addr := c.peers[peerID]
	var newConn net.Conn
	var err error

	for i := 0; i < 5; i++ {
		newConn, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			break
		}
		c.log("Failed to connect to peer %d (attempt %d): %v", peerID, i+1, err)
		time.Sleep(100 * time.Millisecond)
	}

	if err != nil {
		return nil, err
	}

	c.peerConns[peerID] = newConn
	c.log("Connected to peer %d at %s", peerID, addr)
	return newConn, nil
}

func (c *ChainNode) sendTo(peerID int, data []byte) error {
	conn, err := c.connectToPeer(peerID)
	if err != nil {
		c.log("Failed to connect to peer %d: %v", peerID, err)
		return err
	}

	return c.sendToConn(conn, data)
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
// assigning chain sequence numbers to guarantee FIFO ordering.
func (c *ChainNode) putHandler() {
	for msg := range c.putCh {
		c.log("PUT key=%s value=%s seq=%d", msg.Key, msg.Value, msg.Seq)

		c.mu.Lock()
		c.state[msg.Key] = msg.Value
		c.mu.Unlock()

		if c.isTail {
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

// processChainPayload applies the inner message to state and forwards
// the original wrapped data (preserving the head's chain seq) to the successor.
func (c *ChainNode) processChainPayload(wrapped []byte) {
	_, payload := DecodeChainForward(wrapped)

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

func (c *ChainNode) handleChainAck(data []byte) {
	msg, err := DecodeMessage(data)
	if err != nil {
		c.log("Failed to decode ChainAck: %v", err)
		return
	}

	if c.isHead {
		// Head: forward ACK to client
		ack := &Message{
			Type: MsgTypeAck,
			Seq:  msg.Seq,
			Key:  msg.Key,
		}

		if connInterface, ok := c.clientConn.Load(msg.ClientAddr); ok {
			if conn, ok := connInterface.(net.Conn); ok {
				c.sendToConn(conn, ack.Encode())
				c.log("Forwarded ACK to client seq=%d", msg.Seq)
				return
			}
		}

		c.log("Failed to find client connection for %s", msg.ClientAddr)
	} else {
		// Middle node: forward ChainAck to predecessor
		c.sendToPredecessor(data)
	}
}

func (c *ChainNode) log(format string, args ...interface{}) {
	if c.debug {
		msg := fmt.Sprintf(format, args...)
		log.Printf("[Node %d] %s", c.me, msg)
	}
}
