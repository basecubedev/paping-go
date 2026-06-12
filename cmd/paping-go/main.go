package main

import (
	"os"

	"github.com/basecubedev/paping-go/internal/app"
)

var version = "dev"

func main() {
	os.Exit(app.Run(os.Args[1:], version))
}
