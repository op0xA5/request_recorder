package main

import (
	"github.com/urfave/cli/v2"
	"log"
	"os"
)

var VERSION = "0.0.1"

func main() {
	log.Default().SetFlags(log.Ltime | log.Lmicroseconds)

	app := cli.NewApp()
	app.Name = "request recorder"
	app.Version = VERSION
	app.Usage = "Request Recorder"
	app.Commands = []*cli.Command{
		serverCmd(),
		clientCmd(),
	}
	err := app.Run(os.Args)
	if err != nil {
		log.Fatalf("%v", err)
	}
}
