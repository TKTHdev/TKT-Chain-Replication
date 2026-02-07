package main

import (
	"fmt"
	"log"
	"net"
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
		go c.handleMessage(buf[:n], remoteAddr)
	}
}

func (c *ChainNode) handleMessage(data []byte, from *net.UDPAddr) {
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
	// Set client address if this is from client (head receives from client)
	if c.isHead && msg.ClientAddr == "" {
		msg.ClientAddr = from.String()
	}

	c.log("PUT key=%s value=%s seq=%d", msg.Key, msg.Value, msg.Seq)

	// Apply to state machine
	c.mu.Lock()
	c.state[msg.Key] = msg.Value
	c.mu.Unlock()

	if c.isTail {
		// Tail: send ACK back to client
		c.sendAckToClient(msg)
	} else {
		// Forward to successor
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

func (c *ChainNode) log(format string, args ...interface{}) {
	if c.debug {
		msg := fmt.Sprintf(format, args...)
		log.Printf("[Node %d] %s", c.me, msg)
	}
}
