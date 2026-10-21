package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"time"
	_ "time/tzdata"
)

const abiVersion = 1

type controlPeer struct {
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"`
}

type config struct {
	DockerSocket   string        `json:"docker_socket"`
	Namespace      string        `json:"sandbox_namespace"`
	CgroupParent   string        `json:"cgroup_parent"`
	StateDir       string        `json:"state_dir"`
	WorkplacesRoot string        `json:"workplaces_root"`
	SocketPath     string        `json:"socket_path"`
	PinPath        string        `json:"pin_path"`
	DailyLimit     uint64        `json:"daily_limit_bytes"`
	GrantChunk     uint64        `json:"grant_chunk_bytes"`
	LowWatermark   uint64        `json:"refill_low_watermark_bytes"`
	ControlPeers   []controlPeer `json:"control_peers"`
}

func defaultConfig() config {
	return config{
		DockerSocket: "/var/run/docker.sock", Namespace: "laxcode",
		CgroupParent: "/sys/fs/cgroup/laxcode.slice", StateDir: "/var/lib/laxcode-manager",
		WorkplacesRoot: "/root/laxcode",
		SocketPath:     "/run/laxcode-manager/control.sock", PinPath: "/sys/fs/bpf/laxcode-manager",
		DailyLimit: 5_000_000_000, GrantChunk: 16 << 20, LowWatermark: 4 << 20,
	}
}

func readConfig(path string) (config, error) {
	c := defaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("trailing configuration data")
	}
	return c, c.validate()
}

func (c config) validate() error {
	for _, p := range []string{c.DockerSocket, c.CgroupParent, c.StateDir, c.SocketPath, c.PinPath, c.WorkplacesRoot} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fmt.Errorf("path must be absolute and clean: %q", p)
		}
	}
	if c.CgroupParent == "/sys/fs/cgroup" || c.CgroupParent == "/sys/fs/cgroup/system.slice" ||
		filepath.Dir(c.CgroupParent) != "/sys/fs/cgroup" {
		return errors.New("cgroup_parent must be a dedicated direct child of /sys/fs/cgroup")
	}
	if c.Namespace == "" || c.LowWatermark == 0 || c.LowWatermark >= c.GrantChunk ||
		c.GrantChunk > c.DailyLimit || c.DailyLimit > 1<<50 {
		return errors.New("require namespace and 0 < low watermark < grant chunk <= daily limit <= 2^50")
	}
	if len(c.ControlPeers) == 0 || len(c.ControlPeers) > 64 {
		return errors.New("require 1..64 exact control peers")
	}
	seen := map[peerKey]bool{}
	for _, p := range c.ControlPeers {
		k, err := p.key()
		if err != nil {
			return err
		}
		if seen[k] {
			return errors.New("duplicate control peer")
		}
		seen[k] = true
	}
	return nil
}

// All IPv4 addresses use mapped IPv6 encoding, including LPM keys.
func addressBytes(a netip.Addr) [16]byte { return a.Unmap().As16() }

type peerKey struct {
	Address [16]byte
	Port    uint16 // host byte order
	Proto   uint8
	Pad     uint8
}

func (p controlPeer) key() (peerKey, error) {
	a, err := netip.ParseAddr(p.Address)
	if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || p.Port == 0 {
		return peerKey{}, fmt.Errorf("invalid control peer address/port: %q", p.Address)
	}
	var proto uint8
	switch p.Protocol {
	case "tcp":
		proto = 6
	case "udp":
		proto = 17
	default:
		return peerKey{}, errors.New("control protocol must be tcp or udp")
	}
	// Metadata and link-local are never valid control exceptions.
	if a.Unmap().IsLinkLocalUnicast() || a.Unmap() == netip.MustParseAddr("100.96.0.96") ||
		a.Unmap() == netip.MustParseAddr("100.100.100.200") {
		return peerKey{}, errors.New("metadata/link-local cannot be a control peer")
	}
	return peerKey{Address: addressBytes(a), Port: p.Port, Proto: proto}, nil
}

var shanghai, _ = time.LoadLocation("Asia/Shanghai")

func dayBounds(now time.Time) (string, time.Time) {
	local := now.In(shanghai)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, shanghai)
	return local.Format(time.DateOnly), next
}

type usage struct {
	Lock                           uint32
	Pad                            uint32
	RX, TX, DropBytes, DropPackets uint64
	NotifiedVersion, LastEventNS   uint64
}
type grant struct{ Bytes, Version uint64 }
type bucketMeta struct{ User, DeadlineNS uint64 }
type quotaEvent struct{ Bucket, Version uint64 }
