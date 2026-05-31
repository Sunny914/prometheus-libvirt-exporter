//go:build !linux

package exporter

func readMountEntries() []mountEntry { return nil }

func resolveDiskStorage(backingFile string) (mountpoint, physicalDevice string) {
	return "", ""
}

func bridgeForInterface(iface string) string { return "" }

func physicalNICForBridge(bridge string) string { return "" }

func hostCPUCount() int { return 0 }
