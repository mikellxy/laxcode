package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type kernelMetadata struct {
	ABI, Ready             uint32
	Instance, DB, Boot     [16]byte
	ConfigHash             [32]byte
	ParentID, LowWatermark uint64
}
type kernel struct {
	objects *ebpf.Collection
	links   []link.Link
	parent  *os.File
	meta    kernelMetadata
	fresh   bool
}

func cgroupID(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var st unix.Statfs_t
	if err = unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	if st.Type != unix.CGROUP2_SUPER_MAGIC {
		return 0, errors.New("cgroup v2 required")
	}
	handle, _, err := unix.NameToHandleAt(int(f.Fd()), "", unix.AT_EMPTY_PATH)
	if err != nil {
		return 0, err
	}
	// kernfs_encode_fh returns FILEID_KERNFS (include/linux/exportfs.h).
	if handle.Type() != 0xfe || len(handle.Bytes()) != 8 {
		return 0, errors.New("unexpected cgroup file handle ABI")
	}
	id := binary.LittleEndian.Uint64(handle.Bytes())
	if id == 0 {
		return 0, errors.New("zero cgroup ID")
	}
	return id, nil
}

func bootNS() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, err
	}
	return uint64(ts.Nano()), nil
}

func openKernel(c config, db uuid.UUID) (_ *kernel, err error) {
	if unsafe.Sizeof(usage{}) != 56 || unsafe.Sizeof(peerKey{}) != 20 || unsafe.Sizeof(kernelMetadata{}) != 104 {
		return nil, errors.New("BPF ABI size mismatch")
	}
	if err = rlimit.RemoveMemlock(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(c.CgroupParent, 0755); err != nil {
		return nil, err
	}
	parentID, err := cgroupID(c.CgroupParent)
	if err != nil {
		return nil, err
	}
	bootRaw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	boot, err := uuid.Parse(strings.TrimSpace(string(bootRaw)))
	if err != nil {
		return nil, err
	}
	peers := append([]controlPeer(nil), c.ControlPeers...)
	sort.Slice(peers, func(i, j int) bool { return fmt.Sprint(peers[i]) < fmt.Sprint(peers[j]) })
	data, _ := json.Marshal(struct {
		Peers          []controlPeer
		Rules          []addressRule
		Namespace      string
		WorkplacesRoot string
	}{peers, addressRules, c.Namespace, c.WorkplacesRoot})
	hash := sha256.Sum256(data)
	if err = os.MkdirAll(c.PinPath, 0700); err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err = unix.Statfs(c.PinPath, &fs); err != nil {
		return nil, err
	}
	if fs.Type != unix.BPF_FS_MAGIC {
		return nil, errors.New("pin_path must be on bpffs")
	}
	entries, err := os.ReadDir(c.PinPath)
	if err != nil {
		return nil, err
	}
	k := &kernel{fresh: len(entries) == 0}
	success := false
	defer func() {
		if !success {
			k.close()
		}
	}()
	k.parent, err = os.Open(c.CgroupParent)
	if err != nil {
		return nil, err
	}
	spec, err := loadQuota()
	if err != nil {
		return nil, err
	}
	replacements := map[string]*ebpf.Map{}
	defer func() {
		for _, m := range replacements {
			if m != nil {
				m.Close()
			}
		}
	}()
	if !k.fresh {
		// Load metadata first so any inconsistent pin set can be disabled.
		m, e := ebpf.LoadPinnedMap(filepath.Join(c.PinPath, "metadata"), nil)
		if e != nil {
			return nil, fmt.Errorf("incomplete pinned objects: %w", e)
		}
		replacements["metadata"] = m
		zero := uint32(0)
		if err = m.Lookup(zero, &k.meta); err != nil {
			return nil, err
		}
		disable := func() { k.meta.Ready = 0; _ = m.Update(zero, k.meta, ebpf.UpdateExist) }
		if k.meta.ABI != abiVersion || k.meta.DB != [16]byte(db) || k.meta.Boot != [16]byte(boot) ||
			k.meta.ParentID != parentID || k.meta.ConfigHash != hash || k.meta.Ready > 1 {
			disable()
			return nil, errors.New("pinned ABI/DB/boot/parent/config mismatch; guard disabled, inspect state")
		}
		for name := range spec.Maps {
			if name == "metadata" {
				continue
			}
			replacements[name], err = ebpf.LoadPinnedMap(filepath.Join(c.PinPath, name), nil)
			if err != nil {
				disable()
				return nil, fmt.Errorf("incomplete pinned map %s: %w", name, err)
			}
		}
	}
	k.objects, err = ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{MapReplacements: replacements})
	if err != nil {
		return nil, fmt.Errorf("load BPF: %w", err)
	}
	if k.fresh {
		k.meta = kernelMetadata{ABI: abiVersion, Instance: uuid.New(), DB: db, Boot: boot, ConfigHash: hash, ParentID: parentID, LowWatermark: c.LowWatermark}
		if err = k.objects.Maps["metadata"].Update(uint32(0), k.meta, ebpf.UpdateNoExist); err != nil {
			return nil, err
		}
		for name, m := range k.objects.Maps {
			if err = m.Pin(filepath.Join(c.PinPath, name)); err != nil {
				return nil, err
			}
		}
		for _, r := range addressRules {
			if err = k.objects.Maps["address_class"].Update(prefixKey(r.Prefix), r.Public, ebpf.UpdateNoExist); err != nil {
				return nil, err
			}
		}
		for _, p := range c.ControlPeers {
			key, _ := p.key()
			if err = k.objects.Maps["control_peers"].Update(key, uint32(1), ebpf.UpdateNoExist); err != nil {
				return nil, err
			}
		}
	}
	for _, item := range []struct {
		name   string
		attach ebpf.AttachType
	}{{"ingress", ebpf.AttachCGroupInetIngress}, {"egress", ebpf.AttachCGroupInetEgress}} {
		path := filepath.Join(c.PinPath, item.name+"_link")
		var ln link.Link
		if k.fresh {
			q, e := link.QueryPrograms(link.QueryOptions{Target: int(k.parent.Fd()), Attach: item.attach})
			if e != nil {
				return nil, e
			}
			if len(q.Programs) != 0 {
				return nil, errors.New("dedicated parent already has BPF programs; refusing duplicate accounting")
			}
			// Require true pinned bpf_link support; never fall back to detachable legacy hooks.
			ln, err = link.AttachRawLink(link.RawLinkOptions{Target: int(k.parent.Fd()), Program: k.objects.Programs[item.name], Attach: item.attach})
			if err != nil {
				return nil, err
			}
			k.links = append(k.links, ln)
			if err = ln.Pin(path); err != nil {
				return nil, err
			}
		} else {
			ln, err = link.LoadPinnedLink(path, nil)
			if err != nil {
				k.disable()
				return nil, fmt.Errorf("missing guard link: %w", err)
			}
			k.links = append(k.links, ln)
			info, e := ln.Info()
			if e != nil {
				return nil, e
			}
			if info.Cgroup() == nil || info.Cgroup().CgroupId != parentID || uint32(info.Cgroup().AttachType) != uint32(item.attach) {
				k.disable()
				return nil, errors.New("pinned link belongs to another target")
			}
			q, e := link.QueryPrograms(link.QueryOptions{Target: int(k.parent.Fd()), Attach: item.attach})
			if e != nil {
				return nil, e
			}
			if len(q.Programs) != 1 || q.Programs[0].ID != info.Program {
				k.disable()
				return nil, errors.New("unexpected programs on managed cgroup")
			}
			if err = ln.Update(k.objects.Programs[item.name]); err != nil {
				return nil, err
			}
		}
	}
	success = true
	return k, nil
}
func (k *kernel) disable() {
	k.meta.Ready = 0
	_ = k.objects.Maps["metadata"].Update(uint32(0), k.meta, ebpf.UpdateExist)
}
func (k *kernel) enable(low uint64) error {
	k.meta.Ready = 1
	k.meta.LowWatermark = low
	return k.objects.Maps["metadata"].Update(uint32(0), k.meta, ebpf.UpdateExist)
}
func (k *kernel) close() {
	for _, ln := range k.links {
		_ = ln.Close()
	}
	if k.objects != nil {
		k.objects.Close()
	}
	if k.parent != nil {
		_ = k.parent.Close()
	}
}
func (k *kernel) snapshot(id uint64) (usage, grant, error) {
	var u usage
	var g grant
	err := k.objects.Maps["usage"].LookupWithFlags(id, &u, ebpf.LookupLock)
	if err != nil {
		return u, g, err
	}
	err = k.objects.Maps["grants"].Lookup(id, &g)
	return u, g, err
}
func (k *kernel) initialize(e epoch) error {
	var existing usage
	err := k.objects.Maps["usage"].LookupWithFlags(e.ID, &existing, ebpf.LookupLock)
	if err == nil {
		var bm bucketMeta
		if err = k.objects.Maps["bucket_meta"].Lookup(e.ID, &bm); err != nil {
			return err
		}
		if bm.User != e.User || bm.DeadlineNS != e.Deadline {
			return errors.New("bucket identity mismatch")
		}
		return nil
	}
	if !errors.Is(err, ebpf.ErrKeyNotExist) {
		return err
	}
	// Only a zero-authorized, unpublished bucket can safely get fresh counters.
	if e.Granted != 0 || e.RX != 0 || e.TX != 0 {
		return errors.New("active kernel counters missing; refusing restoration from DB usage")
	}
	var pointer uint64
	if k.objects.Maps["user_bucket"].Lookup(e.User, &pointer) == nil && pointer == e.ID {
		return errors.New("published bucket counters missing")
	}
	if err = k.objects.Maps["bucket_meta"].Update(e.ID, bucketMeta{e.User, e.Deadline}, ebpf.UpdateAny); err != nil {
		return err
	}
	if err = k.objects.Maps["grants"].Update(e.ID, grant{e.Granted, e.Version}, ebpf.UpdateAny); err != nil {
		return err
	}
	return k.objects.Maps["usage"].Update(e.ID, usage{}, ebpf.UpdateNoExist)
}
