package main

import (
	"bytes"
	"encoding/binary"
)

const (
	MsgTypePut = iota
	MsgTypeGet
	MsgTypeAck
	MsgTypeResponse
	MsgTypeBatchPut
	MsgTypeChainForward
)

type Message struct {
	Type       uint8
	Seq        uint64
	Key        string
	Value      string
	ClientAddr string // client address for response
}

func (m *Message) Encode() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, m.Type)
	binary.Write(buf, binary.BigEndian, m.Seq)

	keyBytes := []byte(m.Key)
	binary.Write(buf, binary.BigEndian, uint16(len(keyBytes)))
	buf.Write(keyBytes)

	valBytes := []byte(m.Value)
	binary.Write(buf, binary.BigEndian, uint16(len(valBytes)))
	buf.Write(valBytes)

	addrBytes := []byte(m.ClientAddr)
	binary.Write(buf, binary.BigEndian, uint16(len(addrBytes)))
	buf.Write(addrBytes)

	return buf.Bytes()
}

func DecodeMessage(data []byte) (*Message, error) {
	buf := bytes.NewReader(data)
	m := &Message{}

	binary.Read(buf, binary.BigEndian, &m.Type)
	binary.Read(buf, binary.BigEndian, &m.Seq)

	var keyLen uint16
	binary.Read(buf, binary.BigEndian, &keyLen)
	keyBytes := make([]byte, keyLen)
	buf.Read(keyBytes)
	m.Key = string(keyBytes)

	var valLen uint16
	binary.Read(buf, binary.BigEndian, &valLen)
	valBytes := make([]byte, valLen)
	buf.Read(valBytes)
	m.Value = string(valBytes)

	var addrLen uint16
	binary.Read(buf, binary.BigEndian, &addrLen)
	addrBytes := make([]byte, addrLen)
	buf.Read(addrBytes)
	m.ClientAddr = string(addrBytes)

	return m, nil
}

// EncodeBatch encodes multiple messages into a single UDP packet.
// Format: [Type:1][Count:2][MsgLen1:2][Msg1][MsgLen2:2][Msg2]...
func EncodeBatch(msgs []*Message) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint8(MsgTypeBatchPut))
	binary.Write(buf, binary.BigEndian, uint16(len(msgs)))
	for _, m := range msgs {
		encoded := m.Encode()
		binary.Write(buf, binary.BigEndian, uint16(len(encoded)))
		buf.Write(encoded)
	}
	return buf.Bytes()
}

// DecodeBatch decodes a batch packet into individual messages.
func DecodeBatch(data []byte) ([]*Message, error) {
	buf := bytes.NewReader(data)

	var msgType uint8
	binary.Read(buf, binary.BigEndian, &msgType)

	var count uint16
	binary.Read(buf, binary.BigEndian, &count)

	msgs := make([]*Message, 0, count)
	for i := 0; i < int(count); i++ {
		var msgLen uint16
		binary.Read(buf, binary.BigEndian, &msgLen)
		msgBytes := make([]byte, msgLen)
		buf.Read(msgBytes)
		m, err := DecodeMessage(msgBytes)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// EncodeChainForward wraps a payload with a chain sequence number.
// Format: [MsgTypeChainForward:1][chainSeq:8][payload...]
func EncodeChainForward(chainSeq uint64, payload []byte) []byte {
	buf := make([]byte, 1+8+len(payload))
	buf[0] = MsgTypeChainForward
	binary.BigEndian.PutUint64(buf[1:9], chainSeq)
	copy(buf[9:], payload)
	return buf
}

// DecodeChainForward extracts the chain sequence number and inner payload.
func DecodeChainForward(data []byte) (uint64, []byte) {
	chainSeq := binary.BigEndian.Uint64(data[1:9])
	return chainSeq, data[9:]
}
