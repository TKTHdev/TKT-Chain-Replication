package main

import (
	"fmt"
	"net"
)

func (c *ChainNode) listen() error {
	addr, err := net.ResolveUDPAddr("udp", c.peers[c.me])
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
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
	c.log("Received %d bytes from %s", len(data), from)
	// TODO: parse and dispatch message
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
		fmt.Printf("[Node %d] %s\n", c.me, msg)
	}
}
