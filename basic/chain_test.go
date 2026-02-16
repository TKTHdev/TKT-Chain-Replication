package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	NumTestClients = 512
	NumTestWrites  = 2000
)

func writeTestConfig(t *testing.T, basePort int) string {
	t.Helper()
	nodes := []Node{
		{ID: 0, IP: "127.0.0.1", Port: basePort, Role: "client"},
		{ID: 1, IP: "127.0.0.1", Port: basePort + 1, Role: "server"},
		{ID: 2, IP: "127.0.0.1", Port: basePort + 2, Role: "server"},
		{ID: 3, IP: "127.0.0.1", Port: basePort + 3, Role: "server"},
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.conf")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startTestCluster(t *testing.T, confPath string) []*ChainNode {
	t.Helper()
	nodes := make([]*ChainNode, 3)
	for i := range nodes {
		nodes[i] = NewChainNode(i+1, confPath, false)
		go nodes[i].listen()
		if nodes[i].isHead {
			go nodes[i].putHandler()
		}
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			n.udpConn.Close()
		}
	})
	time.Sleep(100 * time.Millisecond)
	return nodes
}

func startTestClient(t *testing.T, confPath string) *Client {
	t.Helper()
	client := NewClient(confPath, 100, 1, false)
	go client.receiveLoop()
	t.Cleanup(func() { client.udpConn.Close() })
	time.Sleep(50 * time.Millisecond)
	return client
}

func snapshotState(node *ChainNode) map[string]string {
	node.mu.RLock()
	defer node.mu.RUnlock()
	snap := make(map[string]string, len(node.state))
	for k, v := range node.state {
		snap[k] = v
	}
	return snap
}

// assertConsistent checks that all nodes have identical KV state.
func assertConsistent(t *testing.T, nodes []*ChainNode) {
	t.Helper()
	states := make([]map[string]string, len(nodes))
	for i, n := range nodes {
		states[i] = snapshotState(n)
	}

	// Collect all keys across all nodes.
	allKeys := make(map[string]struct{})
	for _, s := range states {
		for k := range s {
			allKeys[k] = struct{}{}
		}
	}

	for k := range allKeys {
		headVal := states[0][k]
		for i := 1; i < len(states); i++ {
			if states[i][k] != headVal {
				t.Errorf("state divergence key=%q: node %d has %q, node %d has %q",
					k, 1, headVal, i+1, states[i][k])
			}
		}
	}
}

// TestConsistencySequential sends writes one at a time (waiting for ACK)
// and verifies all nodes converge to the same state.
func TestConsistencySequential(t *testing.T) {
	confPath := writeTestConfig(t, 16100)
	nodes := startTestCluster(t, confPath)
	client := startTestClient(t, confPath)

	writes := []struct{ key, value string }{
		{"x", "1"}, {"y", "2"}, {"x", "3"}, {"z", "4"}, {"y", "5"},
	}
	for _, w := range writes {
		if ok := client.Put(w.key, w.value); !ok {
			t.Fatalf("PUT %s=%s failed (timeout)", w.key, w.value)
		}
	}

	time.Sleep(100 * time.Millisecond)

	// All nodes should have the deterministic final state.
	expected := map[string]string{"x": "3", "y": "5", "z": "4"}
	for i, node := range nodes {
		state := snapshotState(node)
		for k, v := range expected {
			if state[k] != v {
				t.Errorf("node %d: state[%q] = %q, want %q", i+1, k, state[k], v)
			}
		}
	}
}

// TestConsistencyConcurrent sends many concurrent writes from multiple
// goroutines and checks that all nodes end up with identical state.
//
// This is the critical test for FIFO violations: if messages are reordered
// between nodes, different replicas will apply conflicting writes in
// different orders and diverge.
func TestConsistencyConcurrent(t *testing.T) {
	confPath := writeTestConfig(t, 16200)
	nodes := startTestCluster(t, confPath)
	client := startTestClient(t, confPath)

	var wg sync.WaitGroup
	for w := 0; w < NumTestClients; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < NumTestWrites; i++ {
				key := fmt.Sprintf("k%d", i%5) // 5 keys, heavy contention
				val := fmt.Sprintf("w%d-i%d", w, i)
				client.Put(key, val)
			}
		}(w)
	}
	wg.Wait()

	time.Sleep(200 * time.Millisecond)
	assertConsistent(t, nodes)
}
