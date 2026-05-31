//go:build linux

package exporter

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type mountEntry struct {
	deviceID   string
	mountpoint string
}

func readMountEntries() []mountEntry {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer file.Close()

	var mounts []mountEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		dashIdx := strings.Index(line, " - ")
		if dashIdx < 0 {
			continue
		}
		before := strings.Fields(line[:dashIdx])
		if len(before) < 5 {
			continue
		}
		mounts = append(mounts, mountEntry{
			deviceID:   before[2],
			mountpoint: before[4],
		})
	}
	return mounts
}

func resolveDiskStorage(backingFile string) (mountpoint, physicalDevice string) {
	mounts := cachedMounts()
	if len(mounts) == 0 {
		return "", ""
	}

	absPath, err := filepath.Abs(backingFile)
	if err != nil {
		absPath = backingFile
	}

	var best mountEntry
	var bestLen int
	for _, m := range mounts {
		if m.mountpoint == "" {
			continue
		}
		mp := m.mountpoint
		if mp != "/" && !strings.HasSuffix(mp, "/") {
			mp += "/"
		}
		path := absPath
		if path != "/" && !strings.HasSuffix(path, "/") {
			path += "/"
		}
		if strings.HasPrefix(path, mp) || (m.mountpoint == "/" && strings.HasPrefix(absPath, "/")) {
			if len(m.mountpoint) > bestLen {
				best = m
				bestLen = len(m.mountpoint)
			}
		}
	}
	if bestLen == 0 {
		return "", ""
	}

	mountpoint = best.mountpoint
	physicalDevice = deviceNameFromID(best.deviceID)
	if physicalDevice != "" {
		physicalDevice = stripPartitionSuffix(physicalDevice)
	}
	return mountpoint, physicalDevice
}

func deviceNameFromID(deviceID string) string {
	parts := strings.Split(deviceID, ":")
	if len(parts) != 2 {
		return deviceID
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return deviceID
	}
	link := filepath.Join("/sys/dev/block", fmt.Sprintf("%d:%d", major, minor))
	target, err := os.Readlink(link)
	if err == nil {
		return filepath.Base(target)
	}
	return lookupProcPartitions(major, minor)
}

func lookupProcPartitions(major, minor int) string {
	file, err := os.Open("/proc/partitions")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		m, _ := strconv.Atoi(fields[0])
		mi, _ := strconv.Atoi(fields[1])
		if m == major && mi == minor {
			return fields[3]
		}
	}
	return ""
}

func stripPartitionSuffix(name string) string {
	re := regexp.MustCompile(`p?\d+$`)
	return re.ReplaceAllString(name, "")
}

func bridgeForInterface(iface string) string {
	link := filepath.Join("/sys/class/net", iface, "brport", "bridge")
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func physicalNICForBridge(bridge string) string {
	if bridge == "" {
		return ""
	}
	bridgePortsPath := filepath.Join("/sys/class/net", bridge, "brif")
	entries, err := os.ReadDir(bridgePortsPath)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "tap") {
				continue
			}
			if isLikelyPhysicalNIC(name) {
				return name
			}
		}
	}

	// NAT/libvirt bridges (virbr*) usually have no physical port in brif; use default route NIC.
	if strings.HasPrefix(bridge, "virbr") {
		return defaultRouteInterface()
	}

	// Linux bridges may expose uplink via lower_* symlinks.
	lowerPath := filepath.Join("/sys/class/net", bridge, "lower_*")
	matches, _ := filepath.Glob(lowerPath)
	for _, match := range matches {
		name := filepath.Base(match)
		if isLikelyPhysicalNIC(name) {
			return name
		}
	}
	return ""
}

func defaultRouteInterface() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

func hostCPUCount() int {
	return runtime.NumCPU()
}

func isLikelyPhysicalNIC(name string) bool {
	if name == "" || strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "tap") ||
		strings.HasPrefix(name, "virbr") || strings.HasPrefix(name, "docker") ||
		strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth") {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "device"))
	return err == nil
}
