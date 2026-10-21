//go:build !linux

package main

import "log"

func main() { log.Fatal("laxcode-manager requires Linux with cgroup v2") }
