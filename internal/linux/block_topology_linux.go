//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// blockName is a kernel block device name, such as sda, sdb1, or dm-0: the
// entry under /sys/class/block and under /dev.
type blockName string

// blockTopology records the kernel's "X depends on Y" relationship.  Besides
// partitions, Linux exposes stacked block relationships from both ends as
// slaves (dependencies) and holders (dependants).
type blockTopology struct {
	byNumber map[devNumber]blockName
	depends  map[blockName]map[blockName]bool
}

func readBlockTopology(classRoot string) (*blockTopology, error) {
	// This snapshot is intentionally read synchronously and is never cached.
	// Mount, swap, partition, holder, and slave relationships are authorization
	// inputs: returning before the complete current sysfs view is available, or
	// reusing an older view, could authorize a device whose topology changed.
	r := topologyReader{root: classRoot}
	if err := r.indexBlockDevices(); err != nil {
		return nil, err
	}
	for name := range r.t.depends {
		if err := r.linkDevice(name); err != nil {
			return nil, err
		}
	}
	if err := r.t.validateAcyclic(); err != nil {
		return nil, err
	}
	return r.t, nil
}

// topologyReader builds a blockTopology from one sysfs block class directory.
type topologyReader struct {
	root string
	t    *blockTopology
}

// dir is name's entry in the class directory.
func (r topologyReader) dir(name blockName) string { return filepath.Join(r.root, string(name)) }

// indexBlockDevices records every device under the class root by device
// number, so the relation pass can reject any edge pointing outside the
// snapshot.
func (r *topologyReader) indexBlockDevices() error {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return err
	}
	t := &blockTopology{byNumber: map[devNumber]blockName{}, depends: map[blockName]map[blockName]bool{}}
	for _, entry := range entries {
		name := blockName(entry.Name())
		number, err := readDeviceNumber(filepath.Join(r.dir(name), "dev"))
		if err != nil {
			return fmt.Errorf("read block topology for %s: %w", name, err)
		}
		if _, duplicate := t.byNumber[number]; duplicate {
			return fmt.Errorf("duplicate block device number %s", number)
		}
		t.byNumber[number] = name
		t.depends[name] = map[blockName]bool{}
	}
	r.t = t
	return nil
}

// linkDevice records name's parent disk, when name is a partition, plus the
// stacked relations sysfs reports from both ends.
func (r topologyReader) linkDevice(name blockName) error {
	if err := r.linkPartitionParent(name); err != nil {
		return err
	}
	if err := r.readRelations(name, slavesRelation); err != nil {
		return err
	}
	return r.readRelations(name, holdersRelation)
}

func (r topologyReader) linkPartitionParent(name blockName) error {
	if !exists(filepath.Join(r.dir(name), "partition")) {
		return nil
	}
	real, err := filepath.EvalSymlinks(r.dir(name))
	if err != nil {
		return fmt.Errorf("resolve partition %s: %w", name, err)
	}
	parent := blockName(filepath.Base(filepath.Dir(real)))
	if _, ok := r.t.depends[parent]; !ok || parent == name {
		return fmt.Errorf("invalid parent for partition %s", name)
	}
	r.t.depends[name][parent] = true
	return nil
}

// relation is a sysfs directory of stacked block relations.
type relation struct {
	directory string
	// dependencies is true when the entries are what the device depends on,
	// false when they are its dependants.
	dependencies bool
}

var (
	slavesRelation  = relation{directory: "slaves", dependencies: true}
	holdersRelation = relation{directory: "holders", dependencies: false}
)

func (r topologyReader) readRelations(name blockName, rel relation) error {
	entries, err := os.ReadDir(filepath.Join(r.dir(name), rel.directory))
	if errors.Is(err, os.ErrNotExist) { // Older/fake sysfs trees may omit an empty relation directory.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s for %s: %w", rel.directory, name, err)
	}
	for _, entry := range entries {
		related := blockName(entry.Name())
		if _, ok := r.t.depends[related]; !ok || related == name {
			return fmt.Errorf("invalid %s relation %s -> %s", rel.directory, name, related)
		}
		if rel.dependencies {
			r.t.depends[name][related] = true
		} else {
			r.t.depends[related][name] = true
		}
	}
	return nil
}

// validateAcyclic walks the complete snapshot, not only the paths later reached
// by a particular candidate.  A corrupted cycle must never be hidden by an
// earlier successful match during a safety query.
func (t *blockTopology) validateAcyclic() error {
	for name := range t.depends {
		if _, err := t.dependsOn(name, ""); err != nil {
			return err
		}
	}
	return nil
}

// dependsOn returns whether start is target or ultimately backed by target.
// A cycle means the topology cannot be trusted and is therefore an error.
func (t *blockTopology) dependsOn(start, target blockName) (bool, error) {
	if _, ok := t.depends[start]; !ok {
		return false, fmt.Errorf("unknown block device %q", start)
	}
	visiting, done := map[blockName]bool{}, map[blockName]bool{}
	var walk func(blockName) (bool, error)
	walk = func(name blockName) (bool, error) {
		if visiting[name] {
			return false, fmt.Errorf("cyclic block topology at %s", name)
		}
		if done[name] {
			return false, nil
		}
		if name == target {
			return true, nil
		}
		visiting[name] = true
		for dependency := range t.depends[name] {
			found, err := walk(dependency)
			if err != nil || found {
				return found, err
			}
		}
		delete(visiting, name)
		done[name] = true
		return false, nil
	}
	return walk(start)
}

func (t *blockTopology) nameForNumber(number devNumber) (blockName, error) {
	name, ok := t.byNumber[number]
	if !ok {
		return "", fmt.Errorf("unknown block device %s", number)
	}
	return name, nil
}

// swapDevice is a path from /proc/swaps and the device directory, normally
// /dev, that a swap partition's node lives in.
type swapDevice struct {
	path, devRoot string
}

func (t *blockTopology) nameForSwap(s swapDevice) (blockName, bool, error) {
	if name := blockName(filepath.Base(filepath.Clean(s.path))); t.depends[name] != nil {
		return name, true, nil
	}
	info, err := os.Stat(s.path)
	if err != nil {
		if s.underDeviceRoot() {
			return "", false, fmt.Errorf("cannot resolve swap device %q: %w", s.path, err)
		}
		return "", false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		// Regular swap files are protected through the filesystem mount.
		return "", false, nil
	}
	name, err := t.nameForNumber(rdevNumber(stat))
	return name, err == nil, err
}

// rdevNumber is the device number of the special file st describes.
func rdevNumber(st *syscall.Stat_t) devNumber {
	rdev := uint64(st.Rdev)
	return devNumber{uint32(unix.Major(rdev)), uint32(unix.Minor(rdev))}
}

// underDeviceRoot reports whether the path names an entry inside devRoot, such
// as /dev/sdb1 or /dev/mapper/swap. A swap path there that cannot be stat'd is
// a device we failed to resolve and must not silently ignore; a path anywhere
// else is an ordinary swap file.
func (s swapDevice) underDeviceRoot() bool {
	rel, err := filepath.Rel(s.devRoot, s.path)
	if err != nil {
		return false
	}
	return rel != "." && filepath.IsLocal(rel)
}
