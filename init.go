package main

import (
	"os"

	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "chain",
		Usage: "Chain Replication implementation",
		Commands: []*cli.Command{
			{
				Name:  "start",
				Usage: "Start the chain replication node",
				Action: func(c *cli.Context) error {
					id := c.Int("id")
					conf := c.String("conf")
					debug := c.Bool("debug")
					writeBatchSize := c.Int("write-batch-size")

					node := NewChainNode(id, conf, debug, writeBatchSize)
					node.Run()
					return nil
				},
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:     "id",
						Usage:    "Node ID",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "conf",
						Usage: "Path to config file",
						Value: "cluster.conf",
					},
					&cli.BoolFlag{
						Name:  "debug",
						Usage: "Enable debug logging",
						Value: false,
					},
					&cli.IntFlag{
						Name:  "write-batch-size",
						Usage: "Number of PUTs to batch before forwarding",
						Value: 1,
					},
				},
			},
			{
				Name:  "client",
				Usage: "Start the client node",
				Action: func(c *cli.Context) error {
					conf := c.String("conf")
					debug := c.Bool("debug")
					workers := c.Int("workers")
					workloadStr := c.String("workload")
					writeBatchSize := c.Int("write-batch-size")

					workload := 50
					switch workloadStr {
					case "ycsb-a":
						workload = 50
					case "ycsb-b":
						workload = 5
					case "ycsb-c":
						workload = 0
					}

					client := NewClient(conf, workload, workers, debug, writeBatchSize)
					client.Run()
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "conf",
						Usage: "Path to config file",
						Value: "cluster.conf",
					},
					&cli.IntFlag{
						Name:  "workers",
						Usage: "Number of concurrent workers",
						Value: 1,
					},
					&cli.StringFlag{
						Name:  "workload",
						Usage: "Workload type (ycsb-a, ycsb-b, ycsb-c)",
						Value: "ycsb-a",
					},
					&cli.BoolFlag{
						Name:  "debug",
						Usage: "Enable debug logging",
						Value: false,
					},
					&cli.IntFlag{
						Name:  "write-batch-size",
						Usage: "Write batch size (for CSV output)",
						Value: 1,
					},
				},
			},
		},
	}
	if err := app.Run(os.Args); err != nil {
		panic(err)
	}
}
