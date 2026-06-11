//go:build !linux

package exporter

import "regexp"

type mountEntry struct {
	deviceID   string
	mountpoint string
}

func readMountEntries() []mountEntry { return nil }

func resolveDiskStorage(backingFile string) (mountpoint, physicalDevice string) {
	return "", ""
}

func bridgeForInterface(iface string) string { return "" }

func physicalNICForBridge(bridge string) string { return "" }

func hostCPUCount() int { return 0 }

func stripPartitionSuffix(name string) string {
	re := regexp.MustCompile(`p?\d+$`)
	return re.ReplaceAllString(name, "")
}
