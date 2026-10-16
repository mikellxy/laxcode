package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mikellxy/laxcode/cmd/run_sse"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func main() {
	if err := config.Parse(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		panic(err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	codeInstance, err := run_sse.AcquireCodeInstanceGuard(homeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer codeInstance.Close()
	logFile, err := configureSlog(appLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize log %s: %v\n", appLogPath, err)
		os.Exit(1)
	}
	defer logFile.Close()
	run_sse.Run(codeInstance)
}
