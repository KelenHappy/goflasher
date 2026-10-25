//go:build linux

package linux

import (
	"bufio"
	"os"
	"strings"
)

// mountTable maps each mounted block device number to its mount points.
type mountTable map[devNumber][]string

// swapTable is the set of active swap paths from /proc/swaps.
type swapTable map[string]bool

func parseMountInfo(path string) (mountTable, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := mountTable{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		p := strings.Fields(s.Text())
		if len(p) < 5 {
			continue
		}
		number, err := parseDevNumber(p[2])
		if err != nil {
			continue
		}
		out[number] = append(out[number], decodeMount(p[4]))
	}
	return out, s.Err()
}

func parseSwaps(path string) (swapTable, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := swapTable{}
	s := bufio.NewScanner(f)
	firstLine := true
	for s.Scan() {
		if firstLine {
			firstLine = false
			continue
		}
		p := strings.Fields(s.Text())
		if len(p) > 0 {
			out[p[0]] = true
		}
	}
	return out, s.Err()
}

func decodeMount(s string) string {
	r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\x5c")
	return r.Replace(s)
}

// pointsBackedBy resolves every mounted block device through the kernel
// topology. This includes mounts layered on partitions, device-mapper, LVM, or
// software RAID rather than only mounts whose source directly names the disk.
func (m mountTable) pointsBackedBy(d blockDevice, topology *blockTopology) ([]string, error) {
	var mountPoints []string
	for number, points := range m {
		backed, err := mountBackedBy(number, d, topology)
		if err != nil {
			return nil, err
		}
		if backed {
			mountPoints = appendUnique(mountPoints, points...)
		}
	}
	return mountPoints, nil
}

func mountBackedBy(number devNumber, d blockDevice, topology *blockTopology) (bool, error) {
	mounted, err := topology.nameForNumber(number)
	if err != nil {
		if number.major == 0 {
			return false, nil
		}
		return false, err
	}
	return topology.dependsOn(mounted, blockName(d.name))
}

func containsCriticalMount(mountPoints []string) bool {
	critical := map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/home": true}
	for _, mountPoint := range mountPoints {
		if critical[mountPoint] {
			return true
		}
	}
	return false
}

// backs reports whether any non-empty mount sits on d or on a device stacked
// on top of it.
func (m mountTable) backs(d blockDevice, topology *blockTopology) (bool, error) {
	for number, points := range m {
		if len(points) == 0 {
			continue
		}
		mounted, err := mountBacksDevice(number, d, topology)
		if err != nil {
			return false, err
		}
		if mounted {
			return true, nil
		}
	}
	return false, nil
}

func mountBacksDevice(number devNumber, d blockDevice, topology *blockTopology) (bool, error) {
	mounted, usable, err := mountedDeviceName(number, topology)
	if err != nil || !usable {
		return false, err
	}
	if number == d.number {
		return true, nil
	}
	return topology.dependsOn(mounted, blockName(d.name))
}

func mountedDeviceName(number devNumber, topology *blockTopology) (blockName, bool, error) {
	name, err := topology.nameForNumber(number)
	if err == nil {
		return name, true, nil
	}
	if number.major == 0 {
		return "", false, nil
	}
	return "", false, err
}

// backs reports whether any swap device under devRoot is d or stacked on it.
func (s swapTable) backs(d blockDevice, devRoot string, topology *blockTopology) (bool, error) {
	for path := range s {
		swapName, block, err := topology.nameForSwap(swapDevice{path: path, devRoot: devRoot})
		if err != nil {
			return false, err
		}
		if !block {
			continue
		}
		backed, err := topology.dependsOn(swapName, blockName(d.name))
		if err != nil {
			return false, err
		}
		if backed {
			return true, nil
		}
	}
	return false, nil
}
