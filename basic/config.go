package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
)

type Node struct {
	ID   int    `json:"id"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Role string `json:"role"`
}

func loadNodes(confPath string) []Node {
	file, err := os.ReadFile(confPath)
	if err != nil {
		log.Fatalf("Failed to read config file: %v", err)
	}

	var nodes []Node
	if err := json.Unmarshal(file, &nodes); err != nil {
		log.Fatalf("Failed to parse config file: %v", err)
	}

	return nodes
}

func parseConfig(confPath string) map[int]string {
	nodes := loadNodes(confPath)

	peerIPs := make(map[int]string)
	for _, node := range nodes {
		if node.Role == "server" {
			peerIPs[node.ID] = fmt.Sprintf("%s:%d", node.IP, node.Port)
		}
	}
	return peerIPs
}

func parseClientAddr(confPath string) string {
	nodes := loadNodes(confPath)

	for _, node := range nodes {
		if node.Role == "client" {
			return fmt.Sprintf("%s:%d", node.IP, node.Port)
		}
	}
	log.Fatalf("No client node found in config file")
	return ""
}

func sortedIDs(peers map[int]string) []int {
	ids := make([]int, 0, len(peers))
	for id := range peers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func indexOf(ids []int, target int) int {
	for i, id := range ids {
		if id == target {
			return i
		}
	}
	return -1
}
