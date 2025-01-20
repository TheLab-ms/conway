// Glider is an agent of Conway that runs in the makerspace LAN.
// Unlike Conway's main process, it isn't reachable from the internet.
// Instead it provides a buffer for data that is reported to Conway, and a cache for the other direction.
package main

import (
	"context"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	ConwayURL   string `env:",required"`
	ConwayToken string `env:",required"`
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "GLIDER_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}

	_ = conf // TODO
	<-context.Background().Done()
}
