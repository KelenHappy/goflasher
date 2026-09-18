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

// blockTopology records the kernel's "X depends on Y" relationship.  Besides
// partitions, Linux exposes stacked block relationships from both ends as
// slaves (dependencies) and holders (dependants).
type blockTopology struct {
	byNumber map[devNumber]string
	depends  map[string]map[string]bool
}

func readBlockTopology(classRoot string) (*blockTopology, error) {
	// This snapshot is intentionally read synchronously and is never cached.
	// Mount, swap, partition, holder, and slave relationships are authorization
	// inputs: returning before the complete current sysfs view is available, or
	// reusing an older view, could authorize a device whose topology changed.
	t, err := indexBlockDevices(classRoot)
	if err != nil {
		return nil, err
	}
	for name := range t.depends {
		if err := t.linkDevice(classRoot, name); err != nil {
			return nil, err
		}
	}
	if err := t.validateAcyclic(); err != nil {
		return nil, err
	}
	return t, nil
}

// indexBlockDevices records every device under classRoot by device number, so
// the relation pass can reject any edge pointing outside the snapshot.
func indexBlockDevices(classRoot string) (*blockTopology, error) {
	entries, err := os.ReadDir(classRoot)
	if err != nil {
		return nil, err
	}
	t := &blockTopology{byNumber: map[devNumber]string{}, depends: map[string]map[string]bool{}}
	for _, entry := range entries {
		name := entry.Name()
		major, minor, err := readDeviceNumber(filepath.Join(classRoot, name, "dev"))
		if err != nil {
			return nil, fmt.Errorf("read block topology for %s: %w", name, err)
		}
		number := devNumber{major, minor}
		if _, duplicate := t.byNumber[number]; duplicate {
			return nil, fmt.Errorf("duplicate block device number %d:%d", major, minor)
		}
		t.byNumber[number] = name
		t.depends[name] = map[string]bool{}
	}
	return t, nil
}

// linkDevice records name's parent disk, when name is a partition, plus the
// stacked relations sysfs reports from both ends.
func (t *blockTopology) linkDevice(classRoot, name string) error {
	if err := t.linkPartitionParent(classRoot, name); err != nil {
		return err
	}
	if err := t.readRelations(classRoot, name, "slaves", true); err != nil {
		return err
	}
	return t.readRelations(classRoot, name, "holders", false)
}

func (t *blockTopology) linkPartitionParent(classRoot, name string) error {
	if !exists(filepath.Join(classRoot, name, "partition")) {
		return nil
	}
	real, err := filepath.EvalSymlinks(filepath.Join(classRoot, name))
	if err != nil {
		return fmt.Errorf("resolve partition %s: %w", name, err)
	}
	parent := filepath.Base(filepath.Dir(real))
	if _, ok := t.depends[parent]; !ok || parent == name {
		return fmt.Errorf("invalid parent for partition %s", name)
	}
	t.depends[name][parent] = true
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

func (t *blockTopology) readRelations(root, name, directory string, dependencies bool) error {
	entries, err := os.ReadDir(filepath.Join(root, name, directory))
	if errors.Is(err, os.ErrNotExist) { // Older/fake sysfs trees may omit an empty relation directory.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s for %s: %w", directory, name, err)
	}
	for _, entry := range entries {
		related := entry.Name()
		if _, ok := t.depends[related]; !ok || related == name {
			return fmt.Errorf("invalid %s relation %s -> %s", directory, name, related)
		}
		if dependencies {
			t.depends[name][related] = true
		} else {
			t.depends[related][name] = true
		}
	}
	return nil
}

// dependsOn returns whether start is target or ultimately backed by target.
// A cycle means the topology cannot be trusted and is therefore an error.
func (t *blockTopology) dependsOn(start, target string) (bool, error) {
	if _, ok := t.depends[start]; !ok {
		return false, fmt.Errorf("unknown block device %q", start)
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var walk func(string) (bool, error)
	walk = func(name string) (bool, error) {
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

func (t *blockTopology) nameForNumber(number devNumber) (string, error) {
	name, ok := t.byNumber[number]
	if !ok {
		return "", fmt.Errorf("unknown block device %d:%d", number.major, number.minor)
	}
	return name, nil
}

func (t *blockTopology) nameForSwapPath(path, devRoot string) (string, bool, error) {
	if name := filepath.Base(filepath.Clean(path)); t.depends[name] != nil {
		return name, true, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if underDeviceRoot(path, devRoot) {
			return "", false, fmt.Errorf("cannot resolve swap device %q: %w", path, err)
		}
		return "", false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		// Regular swap files are protected through the filesystem mount.
		return "", false, nil
	}
	name, err := t.nameForNumber(rdevNumber(uint64(stat.Rdev)))
	return name, err == nil, err
}

func rdevNumber(rdev uint64) devNumber {
	return devNumber{uint32(unix.Major(rdev)), uint32(unix.Minor(rdev))}
}

// underDeviceRoot reports whether path names an entry directly inside devRoot.
// A swap path there that cannot be stat'd is a device we failed to resolve and
// must not silently ignore; a path anywhere else is an ordinary swap file.
func underDeviceRoot(path, devRoot string) bool {
	rel, err := filepath.Rel(filepath.Clean(devRoot), filepath.Clean(path))
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return filepath.Dir(rel) != ".."
}
