package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Defaults, overridable via environment variables:
//
//	TEST_CLIENTS=256 TEST_WRITES=5000 TEST_KEYS=10 go test -v -run TestConsistencyConcurrent
var (
	NumTestClients = envInt("TEST_CLIENTS", 128)
	NumTestWrites  = envInt("TEST_WRITES", 10000)
	NumTestKeys    = envInt("TEST_KEYS", 5)
)

func envInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return def
}

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
	client := NewClient(confPath, 100, 1, 6, false)
	go client.receiveLoop()
	t.Cleanup(func() { client.udpConn.Close() })
	time.Sleep(50 * time.Millisecond)
	return client
}

// snapshotCleanState returns the latest clean value for each key.
func snapshotCleanState(node *ChainNode) map[string]string {
	node.mu.RLock()
	defer node.mu.RUnlock()
	snap := make(map[string]string)
	for k, vl := range node.store {
		lc := vl.LatestClean()
		if lc != nil {
			snap[k] = lc.Value
		}
	}
	return snap
}

// assertConsistent checks that all nodes have identical clean KV state.
func assertConsistent(t *testing.T, nodes []*ChainNode) {
	t.Helper()
	states := make([]map[string]string, len(nodes))
	for i, n := range nodes {
		states[i] = snapshotCleanState(n)
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
		state := snapshotCleanState(node)
		for k, v := range expected {
			if state[k] != v {
				t.Errorf("node %d: state[%q] = %q, want %q", i+1, k, state[k], v)
			}
		}
	}
}

// TestConsistencyConcurrent sends many concurrent writes from multiple
// goroutines and checks that all nodes end up with identical state.
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
				key := fmt.Sprintf("k%d", i%NumTestKeys)
				val := fmt.Sprintf("w%d-i%d", w, i)
				client.Put(key, val)
			}
		}(w)
	}
	wg.Wait()

	time.Sleep(200 * time.Millisecond)
	assertConsistent(t, nodes)
}

// TestReadFromAnyNode writes values and verifies that GET from each individual
// node returns the correct value (CRAQ's key feature).
func TestReadFromAnyNode(t *testing.T) {
	confPath := writeTestConfig(t, 16300)
	nodes := startTestCluster(t, confPath)
	client := startTestClient(t, confPath)

	// Write some data
	writes := map[string]string{"a": "100", "b": "200", "c": "300"}
	for k, v := range writes {
		if ok := client.Put(k, v); !ok {
			t.Fatalf("PUT %s=%s failed", k, v)
		}
	}

	// Wait for chain ACKs to propagate (all versions become clean)
	time.Sleep(200 * time.Millisecond)

	// Send GET directly to each node and verify correct response
	for _, node := range nodes {
		for k, expectedVal := range writes {
			seq := uint64(1000 + node.me*100)
			msg := &Message{
				Type: MsgTypeGet,
				Seq:  seq,
				Key:  k,
			}

			// Create a temporary UDP conn for this test query
			localAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
			conn, err := net.ListenUDP("udp", localAddr)
			if err != nil {
				t.Fatal(err)
			}

			nodeAddr, _ := net.ResolveUDPAddr("udp", node.peers[node.me])
			conn.WriteToUDP(msg.Encode(), nodeAddr)

			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 65535)
			n, _, err := conn.ReadFromUDP(buf)
			conn.Close()
			if err != nil {
				t.Fatalf("node %d: GET %s timed out: %v", node.me, k, err)
			}

			resp, err := DecodeMessage(buf[:n])
			if err != nil {
				t.Fatalf("node %d: failed to decode response: %v", node.me, err)
			}
			if resp.Value != expectedVal {
				t.Errorf("node %d: GET %s = %q, want %q", node.me, k, resp.Value, expectedVal)
			}
		}
	}
}

// TestACKBackPropagation writes values and verifies that after completion,
// all nodes have the latest version marked as clean.
func TestACKBackPropagation(t *testing.T) {
	confPath := writeTestConfig(t, 16400)
	nodes := startTestCluster(t, confPath)
	client := startTestClient(t, confPath)

	// Write some data
	writes := map[string]string{"x": "10", "y": "20"}
	for k, v := range writes {
		if ok := client.Put(k, v); !ok {
			t.Fatalf("PUT %s=%s failed", k, v)
		}
	}

	// Wait for chain ACKs to propagate
	time.Sleep(200 * time.Millisecond)

	// Check all nodes: latest version for each key should be clean
	for _, node := range nodes {
		node.mu.RLock()
		for k, expected := range writes {
			vl, exists := node.store[k]
			if !exists {
				node.mu.RUnlock()
				t.Fatalf("node %d: key %q not in store", node.me, k)
			}
			latest := vl.Latest()
			if latest == nil {
				node.mu.RUnlock()
				t.Fatalf("node %d: key %q has no versions", node.me, k)
			}
			if !latest.Clean {
				node.mu.RUnlock()
				t.Errorf("node %d: key %q latest version %d is not clean", node.me, k, latest.Num)
			}
			if latest.Value != expected {
				node.mu.RUnlock()
				t.Errorf("node %d: key %q latest value = %q, want %q", node.me, k, latest.Value, expected)
			}
		}
		node.mu.RUnlock()
	}

	// Also verify that old versions were GC'd (only latest clean should remain)
	for _, node := range nodes {
		node.mu.RLock()
		for k, vl := range node.store {
			if len(vl.Versions) != 1 {
				t.Errorf("node %d: key %q has %d versions, want 1 (GC should have cleaned up)",
					node.me, k, len(vl.Versions))
			}
		}
		node.mu.RUnlock()
	}
}
