//go:build linux

package main

// Requires clang-19 with the BPF backend on PATH.
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel -tags linux -cc clang-19 -no-strip quota ebpf/quota.c -- -O2 -g -Wall -Werror -Iebpf
