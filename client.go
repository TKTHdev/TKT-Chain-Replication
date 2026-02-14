package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EXPERIMENT_DURATION = 10 * time.Second
	VALUE_SIZE          = 100
)

type Client struct {
	headConn   net.Conn
	tailConn   net.Conn
	headConnMu sync.Mutex // protects writes to headConn
	tailConnMu sync.Mutex // protects writes to tailConn
	peers      map[int]string
	debug      bool

	// Pending requests (seq -> response channel)
	pending map[uint64]chan *Message
	mu      sync.RWMutex
	nextSeq uint64

	// YCSB config
	workload int // write ratio (50=YCSB-A, 5=YCSB-B, 0=YCSB-C)
	workers  int
}

type WorkerResult struct {
	count    int
	duration time.Duration
}

func NewClient(confPath string, workload int, workers int, debug bool) *Client {
	peers := parseConfig(confPath)
	ids := sortedIDs(peers)

	// Connect to head
	headAddr := peers[ids[0]]
	headConn, err := net.DialTimeout("tcp", headAddr, 5*time.Second)
	if err != nil {
		panic(fmt.Sprintf("Failed to connect to head: %v", err))
	}

	// Connect to tail
	tailAddr := peers[ids[len(ids)-1]]
	tailConn, err := net.DialTimeout("tcp", tailAddr, 5*time.Second)
	if err != nil {
		panic(fmt.Sprintf("Failed to connect to tail: %v", err))
	}

	client := &Client{
		headConn: headConn,
		tailConn: tailConn,
		peers:    peers,
		debug:    debug,
		pending:  make(map[uint64]chan *Message),
		workload: workload,
		workers:  workers,
	}

	return client
}

func (c *Client) Run() {
	fmt.Printf("[Client] Connected to head=%s, tail=%s\n", c.headConn.RemoteAddr(), c.tailConn.RemoteAddr())
	fmt.Printf("[Client] Workload: %d%% writes, Workers: %d\n", c.workload, c.workers)

	// Start receive loops for both connections
	go c.receiveLoop(c.headConn)
	go c.receiveLoop(c.tailConn)

	// Wait for servers to be ready
	time.Sleep(2 * time.Second)

	c.runBenchmark()
}

func (c *Client) receiveLoop(conn net.Conn) {
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

		msg, err := DecodeMessage(data)
		if err != nil {
			c.log("Failed to decode message: %v", err)
			continue
		}

		c.mu.RLock()
		ch, ok := c.pending[msg.Seq]
		c.mu.RUnlock()

		if ok {
			ch <- msg
		}
	}
}

func (c *Client) sendMessage(conn net.Conn, msg *Message) error {
	// Lock the appropriate connection
	if conn == c.headConn {
		c.headConnMu.Lock()
		defer c.headConnMu.Unlock()
	} else if conn == c.tailConn {
		c.tailConnMu.Lock()
		defer c.tailConnMu.Unlock()
	}

	data := msg.Encode()

	// Write length prefix
	msgLen := uint32(len(data))
	if err := binary.Write(conn, binary.BigEndian, msgLen); err != nil {
		return err
	}

	// Write message data
	if _, err := conn.Write(data); err != nil {
		return err
	}

	return nil
}

func (c *Client) Put(key, value string) bool {
	seq := atomic.AddUint64(&c.nextSeq, 1)
	msg := &Message{
		Type:  MsgTypePut,
		Seq:   seq,
		Key:   key,
		Value: value,
	}

	respCh := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[seq] = respCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
	}()

	if err := c.sendMessage(c.headConn, msg); err != nil {
		c.log("Failed to send PUT: %v", err)
		return false
	}

	select {
	case resp := <-respCh:
		return resp.Type == MsgTypeAck
	case <-time.After(5 * time.Second):
		return false
	}
}

func (c *Client) Get(key string) (string, bool) {
	seq := atomic.AddUint64(&c.nextSeq, 1)
	msg := &Message{
		Type: MsgTypeGet,
		Seq:  seq,
		Key:  key,
	}

	respCh := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[seq] = respCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
	}()

	if err := c.sendMessage(c.tailConn, msg); err != nil {
		c.log("Failed to send GET: %v", err)
		return "", false
	}

	select {
	case resp := <-respCh:
		return resp.Value, resp.Type == MsgTypeResponse
	case <-time.After(5 * time.Second):
		return "", false
	}
}

func (c *Client) runBenchmark() {
	c.log("Starting benchmark...")

	ctx, cancel := context.WithTimeout(context.Background(), EXPERIMENT_DURATION)
	defer cancel()

	resultCh := make(chan WorkerResult, c.workers)
	var wg sync.WaitGroup

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultCh <- c.worker(ctx)
		}()
	}

	wg.Wait()
	close(resultCh)

	var totalCount int
	var totalDuration time.Duration
	for res := range resultCh {
		totalCount += res.count
		totalDuration += res.duration
	}

	throughput := float64(totalCount) / EXPERIMENT_DURATION.Seconds()
	avgLatency := float64(0)
	if totalCount > 0 {
		avgLatency = float64(totalDuration.Milliseconds()) / float64(totalCount)
	}

	workloadName := "unknown"
	switch c.workload {
	case 50:
		workloadName = "ycsb-a"
	case 5:
		workloadName = "ycsb-b"
	case 0:
		workloadName = "ycsb-c"
	}

	fmt.Printf("Benchmark completed\n")
	fmt.Printf("Total ops: %d\n", totalCount)
	fmt.Printf("Throughput: %.2f ops/sec\n", throughput)
	fmt.Printf("Avg latency: %.2f ms\n", avgLatency)
	fmt.Printf("RESULT:%s,%d,%.2f,%.2f\n", workloadName, c.workers, throughput, avgLatency)
}

func (c *Client) worker(ctx context.Context) WorkerResult {
	res := WorkerResult{}
	keys := []string{"a", "b", "c", "d", "e", "f"}

	for {
		select {
		case <-ctx.Done():
			return res
		default:
		}

		key := keys[rand.Intn(len(keys))]
		value := randomValue(VALUE_SIZE)

		start := time.Now()
		var ok bool

		if rand.Intn(100) < c.workload {
			ok = c.Put(key, value)
		} else {
			_, ok = c.Get(key)
		}

		if ok {
			res.count++
			res.duration += time.Since(start)
		}
	}
}

func randomValue(size int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, size)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func (c *Client) log(format string, args ...interface{}) {
	if c.debug {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("[Client] %s\n", msg)
	}
}
