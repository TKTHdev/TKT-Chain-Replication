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

					node := NewChainNode(id, conf, debug)
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
				},
			},
		},
	}
	if err := app.Run(os.Args); err != nil {
		panic(err)
	}
}
