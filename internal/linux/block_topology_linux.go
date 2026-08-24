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
	for name := range t.depends {
		if exists(filepath.Join(classRoot, name, "partition")) {
			real, err := filepath.EvalSymlinks(filepath.Join(classRoot, name))
			if err != nil {
				return nil, fmt.Errorf("resolve partition %s: %w", name, err)
			}
			parent := filepath.Base(filepath.Dir(real))
			if _, ok := t.depends[parent]; !ok || parent == name {
				return nil, fmt.Errorf("invalid parent for partition %s", name)
			}
			t.depends[name][parent] = true
		}
		if err := t.readRelations(classRoot, name, "slaves", true); err != nil {
			return nil, err
		}
		if err := t.readRelations(classRoot, name, "holders", false); err != nil {
			return nil, err
		}
	}
	// Validate the complete snapshot, not only paths later reached by a
	// particular candidate.  A corrupted cycle must never be hidden by an
	// earlier successful match during a safety query.
	for name := range t.depends {
		if _, err := t.dependsOn(name, ""); err != nil {
			return nil, err
		}
	}
	return t, nil
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
	if err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Mode&syscall.S_IFMT == syscall.S_IFBLK {
			name, err := t.nameForNumber(devNumber{uint32(unix.Major(uint64(stat.Rdev))), uint32(unix.Minor(uint64(stat.Rdev)))})
			return name, err == nil, err
		}
		// Regular swap files are protected through the filesystem mount.
		return "", false, nil
	}
	rel, relErr := filepath.Rel(filepath.Clean(devRoot), filepath.Clean(path))
	if relErr == nil && rel != ".." && !filepath.IsAbs(rel) && rel != "." && filepath.Dir(rel) != ".." {
		return "", false, fmt.Errorf("cannot resolve swap device %q: %w", path, err)
	}
	return "", false, nil
}
