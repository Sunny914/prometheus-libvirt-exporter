package exporter

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/digitalocean/go-libvirt"
	"github.com/prometheus/client_golang/prometheus"
)

// Topology metric descriptors (additive; existing metrics unchanged).
var (
	libvirtDomainRelationshipInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain_relationship", "info"),
		"Relationship edge for infrastructure topology graphs.",
		[]string{"domain", "relation_type", "source", "target"},
		nil)
	libvirtDomainStorageTopologyInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain_storage_topology", "info"),
		"Virtual disk to backing file, mountpoint, physical device, and storage pool.",
		[]string{"domain", "virtual_disk", "backing_file", "mountpoint", "physical_device", "storage_pool"},
		nil)
	libvirtNetworkTopologyInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "network_topology", "info"),
		"Virtual interface to Linux bridge and physical uplink NIC.",
		[]string{"domain", "virtual_interface", "bridge", "physical_interface"},
		nil)
	libvirtDomainVcpuTopologyInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain_vcpu_topology", "info"),
		"Virtual CPU to host CPU affinity and pinning topology.",
		[]string{"domain", "vcpu", "host_cpu", "pinned", "state"},
		nil)
	libvirtGuestFilesystemInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "guest_filesystem", "info"),
		"Guest filesystem discovered via QEMU guest agent.",
		[]string{"domain", "mountpoint", "filesystem", "device"},
		nil)
	libvirtGuestFilesystemSizeBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "guest_filesystem", "size_bytes"),
		"Total guest filesystem capacity in bytes.",
		[]string{"domain", "mountpoint", "filesystem", "device"},
		nil)
	libvirtGuestFilesystemUsedBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "guest_filesystem", "used_bytes"),
		"Used guest filesystem space in bytes.",
		[]string{"domain", "mountpoint", "filesystem", "device"},
		nil)
	libvirtGuestFilesystemFreeBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "guest_filesystem", "free_bytes"),
		"Free guest filesystem space in bytes.",
		[]string{"domain", "mountpoint", "filesystem", "device"},
		nil)
	libvirtDomainTopologyTimedOutDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "domain_topology", "timed_out"),
		"Whether scraping libvirt domain topology metrics has timed out.",
		[]string{"domain"},
		nil)
)

type relationshipEdge struct {
	relationType string
	source       string
	target       string
}

// guestFilesystem holds parsed guest-agent filesystem data.
type guestFilesystem struct {
	mountpoint      string
	fstype          string
	device          string
	totalBytes      uint64
	usedBytes       uint64
	freeBytes       uint64
	diskAlias       string // e.g. "vda"
	partitionDevice string // e.g. "/dev/vda2" (not normalized)
}

type vcpuTopologyEntry struct {
	vcpu           uint32
	currentHostCPU int32
	state          int32
	affinity       []int
	pinned         bool
}

var (
	mountCacheMu   sync.Mutex
	mountCache     []mountEntry
	mountCacheTime time.Time
)

const mountCacheTTL = 30 * time.Second

func emitRelationshipEdges(ch chan<- prometheus.Metric, promLabels []string, edges []relationshipEdge) {
	for _, edge := range edges {
		if edge.source == "" || edge.target == "" {
			continue
		}
		labels := append(promLabels, edge.relationType, edge.source, edge.target)
		ch <- prometheus.MustNewConstMetric(libvirtDomainRelationshipInfoDesc, prometheus.GaugeValue, 1, labels...)
	}
}

// BuildDomainRelationshipEdges returns graph edges derivable from domain XML.
func BuildDomainRelationshipEdges(domain domainMeta) []relationshipEdge {
	var edges []relationshipEdge
	for _, disk := range domain.libvirtSchema.Devices.Disks {
		if disk.Device == "cdrom" || disk.Device == "fd" || disk.Target.Device == "" {
			continue
		}
		edges = append(edges, relationshipEdge{"attached_disk", domain.domainName, disk.Target.Device})
	}
	for _, iface := range domain.libvirtSchema.Devices.Interfaces {
		if iface.Target.Device == "" {
			continue
		}
		edges = append(edges, relationshipEdge{"attached_interface", domain.domainName, iface.Target.Device})
		bridge := iface.Source.Bridge
		if bridge == "" {
			bridge = bridgeForInterface(iface.Target.Device)
		}
		if bridge != "" {
			edges = append(edges, relationshipEdge{"connected_bridge", iface.Target.Device, bridge})
		}
	}
	return edges
}

// CollectDomainRelationshipInfo emits graph edges from already-parsed domain XML (no extra libvirt calls).
func CollectDomainRelationshipInfo(ch chan<- prometheus.Metric, _ *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, _ time.Duration) (err error, hasTimedOut bool) {
	logger.Debug("collecting domain relationship topology", "domain", domain.domainName)
	edges := BuildDomainRelationshipEdges(domain)
	emitRelationshipEdges(ch, promLabels, edges)
	logger.Debug("domain relationship topology complete", "domain", domain.domainName, "edges", len(edges))
	return nil, false
}

func guestInfoLibvirt(l, guestL *libvirt.Libvirt) *libvirt.Libvirt {
	if guestL != nil {
		return guestL
	}
	return l
}

// CollectGuestFilesystemInfo collects guest filesystem metrics via virDomainGetGuestInfo.
func CollectGuestFilesystemInfo(ch chan<- prometheus.Metric, l *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, timeout time.Duration) (err error, hasTimedOut bool) {
	logger.Debug("collecting guest filesystem topology", "domain", domain.domainName)
	lGuest := guestInfoLibvirt(l, domain.guestLibvirt)

	type guestResult struct {
		params []libvirt.TypedParam
		err    error
	}
	chRes := make(chan guestResult, 1)
	go func() {
		params, guestErr := lGuest.DomainGetGuestInfo(domain.libvirtDomain, uint32(libvirt.DomainGuestInfoFilesystem), 0)
		chRes <- guestResult{params: params, err: guestErr}
	}()

	var result guestResult
	select {
	case result = <-chRes:
	case <-time.After(timeout):
		logger.Debug("guest filesystem collection timed out", "domain", domain.domainName)
		return nil, true
	}
	if result.err != nil {
		logger.Debug("guest filesystem API call failed", "domain", domain.domainName, "msg", result.err)
		return nil, false
	}
	logger.Debug("guest filesystem API returned params", "domain", domain.domainName, "count", len(result.params))

	fss := ParseGuestFilesystemFromParams(result.params)
	logger.Debug("guest filesystem parse complete", "domain", domain.domainName, "filesystems", len(fss))
	if len(fss) == 0 {
		return nil, false
	}

	emitted := 0
	for _, fs := range fss {
		labels := append(promLabels, fs.mountpoint, fs.fstype, fs.device)
		ch <- prometheus.MustNewConstMetric(libvirtGuestFilesystemInfoDesc, prometheus.GaugeValue, 1, labels...)
		ch <- prometheus.MustNewConstMetric(libvirtGuestFilesystemSizeBytesDesc, prometheus.GaugeValue, float64(fs.totalBytes), labels...)
		ch <- prometheus.MustNewConstMetric(libvirtGuestFilesystemUsedBytesDesc, prometheus.GaugeValue, float64(fs.usedBytes), labels...)
		ch <- prometheus.MustNewConstMetric(libvirtGuestFilesystemFreeBytesDesc, prometheus.GaugeValue, float64(fs.freeBytes), labels...)
		emitGuestFilesystemRelationship(ch, promLabels, fs.diskAlias, fs.partitionDevice, fs.mountpoint)
		emitted += 4
	}
	logger.Debug("guest filesystem metrics emitted", "domain", domain.domainName, "metrics", emitted)
	return nil, false
}

// normalizePartitionName converts /dev/vda2 -> vda2, or returns the input if already normalized.
func normalizePartitionName(partitionDevice string) string {
	if partitionDevice == "" {
		return ""
	}
	if strings.HasPrefix(partitionDevice, "/dev/") {
		return partitionDevice[5:] // strip /dev/ prefix
	}
	return partitionDevice
}

func emitGuestFilesystemRelationship(ch chan<- prometheus.Metric, promLabels []string, diskAlias, partitionDevice, mountpoint string) {
	if diskAlias == "" || partitionDevice == "" || mountpoint == "" {
		return
	}
	// Normalize partition name from /dev/vda2 to vda2
	partitionName := normalizePartitionName(partitionDevice)

	// Emit: disk -> partition
	labels := append(promLabels, "has_partition", diskAlias, partitionName)
	ch <- prometheus.MustNewConstMetric(libvirtDomainRelationshipInfoDesc, prometheus.GaugeValue, 1, labels...)

	// Emit: partition -> mountpoint
	labels = append(promLabels, "mounts", partitionName, mountpoint)
	ch <- prometheus.MustNewConstMetric(libvirtDomainRelationshipInfoDesc, prometheus.GaugeValue, 1, labels...)
}

// CollectDomainStorageTopology maps virtual disks to host storage using mountinfo.
func CollectDomainStorageTopology(ch chan<- prometheus.Metric, l *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, _ time.Duration) (err error, hasTimedOut bool) {
	logger.Debug("collecting storage topology", "domain", domain.domainName)
	emitted := 0
	for _, disk := range domain.libvirtSchema.Devices.Disks {
		if disk.Device == "cdrom" || disk.Device == "fd" {
			continue
		}
		backingFile := disk.Source.File
		if backingFile == "" {
			continue
		}

		mountpoint, physicalDevice := resolveDiskStorage(backingFile)
		poolName := lookupStoragePoolName(l, backingFile)

		labels := append(promLabels, disk.Target.Device, backingFile, mountpoint, physicalDevice, poolName)
		ch <- prometheus.MustNewConstMetric(libvirtDomainStorageTopologyInfoDesc, prometheus.GaugeValue, 1, labels...)
		emitStorageRelationships(ch, promLabels, disk.Target.Device, backingFile, physicalDevice, poolName)
		emitted++
		if mountpoint == "" && physicalDevice == "" {
			logger.Debug("storage topology incomplete", "domain", domain.domainName, "file", backingFile)
		}
	}
	logger.Debug("storage topology metrics emitted", "domain", domain.domainName, "metrics", emitted)
	return nil, false
}

func emitStorageRelationships(ch chan<- prometheus.Metric, promLabels []string, virtualDisk, backingFile, physicalDevice, poolName string) {
	emit := func(relationType, source, target string) {
		if source == "" || target == "" {
			return
		}
		labels := append(promLabels, relationType, source, target)
		ch <- prometheus.MustNewConstMetric(libvirtDomainRelationshipInfoDesc, prometheus.GaugeValue, 1, labels...)
	}
	if virtualDisk != "" && backingFile != "" {
		emit("backing_file", virtualDisk, backingFile)
	}
	if backingFile != "" && physicalDevice != "" {
		emit("physical_disk", backingFile, physicalDevice)
	}
	if physicalDevice != "" && poolName != "" {
		emit("storage_pool", physicalDevice, poolName)
	}
}

// CollectDomainNetworkTopology discovers bridge to physical NIC relationships.
func CollectDomainNetworkTopology(ch chan<- prometheus.Metric, _ *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, _ time.Duration) (err error, hasTimedOut bool) {
	logger.Debug("collecting network topology", "domain", domain.domainName)
	emitted := 0
	for _, iface := range domain.libvirtSchema.Devices.Interfaces {
		virtualIface := iface.Target.Device
		if virtualIface == "" {
			continue
		}
		bridge := iface.Source.Bridge
		if bridge == "" {
			bridge = bridgeForInterface(virtualIface)
		}
		physicalIface := physicalNICForBridge(bridge)
		logger.Debug("network topology resolved", "domain", domain.domainName, "interface", virtualIface, "bridge", bridge, "physical_interface", physicalIface)

		labels := append(promLabels, virtualIface, bridge, physicalIface)
		ch <- prometheus.MustNewConstMetric(libvirtNetworkTopologyInfoDesc, prometheus.GaugeValue, 1, labels...)
		if bridge != "" && physicalIface != "" {
			emitRelationshipEdges(ch, promLabels, []relationshipEdge{{"bridge_uplink", bridge, physicalIface}})
		}
		emitted++
	}
	logger.Debug("network topology metrics emitted", "domain", domain.domainName, "metrics", emitted)
	return nil, false
}

// CollectDomainVCPUTopology exposes vCPU to host CPU affinity via libvirt.
func CollectDomainVCPUTopology(ch chan<- prometheus.Metric, l *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, timeout time.Duration) (err error, hasTimedOut bool) {
	logger.Debug("collecting vCPU topology", "domain", domain.domainName)
	maxVcpus, vcpuErr := l.DomainGetVcpusFlags(domain.libvirtDomain, 0)
	if vcpuErr != nil || maxVcpus <= 0 {
		logger.Debug("vCPU topology could not determine vCPU count", "domain", domain.domainName, "msg", vcpuErr)
		return nil, false
	}

	const maplen = 128
	type vcpuResult struct {
		info    []libvirt.VcpuInfo
		cpumaps []byte
		err     error
	}
	chRes := make(chan vcpuResult, 1)
	go func() {
		info, cpumaps, vcpuErr := l.DomainGetVcpus(domain.libvirtDomain, maxVcpus, maplen)
		chRes <- vcpuResult{info: info, cpumaps: cpumaps, err: vcpuErr}
	}()

	var result vcpuResult
	select {
	case result = <-chRes:
	case <-time.After(timeout):
		logger.Debug("vCPU topology collection timed out", "domain", domain.domainName)
		return nil, true
	}
	if result.err != nil {
		logger.Debug("vCPU topology API call failed", "domain", domain.domainName, "msg", result.err)
		return nil, false
	}

	entries := ParseVcpuTopology(result.info, result.cpumaps, maplen, hostCPUCount())
	logger.Debug("vCPU topology parse complete", "domain", domain.domainName, "vcpus", len(entries))

	emitted := 0
	var edges []relationshipEdge
	for _, entry := range entries {
		pinned := "false"
		if entry.pinned {
			pinned = "true"
		}
		state := vcpuStateLabel(entry.state)

		// Emit vCPU topology metrics (backward compatibility)
		hostCPUs := entry.affinity
		if entry.pinned {
			if len(hostCPUs) == 0 && entry.currentHostCPU >= 0 {
				hostCPUs = []int{int(entry.currentHostCPU)}
			}
		} else {
			if entry.currentHostCPU >= 0 {
				hostCPUs = []int{int(entry.currentHostCPU)}
			} else {
				hostCPUs = nil
			}
		}
		for _, hostCPU := range hostCPUs {
			labels := append(promLabels, strconv.FormatUint(uint64(entry.vcpu), 10), strconv.Itoa(hostCPU), pinned, state)
			ch <- prometheus.MustNewConstMetric(libvirtDomainVcpuTopologyInfoDesc, prometheus.GaugeValue, 1, labels...)
			emitted++
		}

		// Emit topology graph edges: VM -> vCPU -> affinity CPUs (use affinity only, not currentHostCPU)
		vcpuName := "vcpu" + strconv.FormatUint(uint64(entry.vcpu), 10)
		edges = append(edges, relationshipEdge{"has_vcpu", domain.domainName, vcpuName})

		// Add has_affinity edges for each CPU in the affinity list
		for _, cpu := range entry.affinity {
			cpuName := "cpu" + strconv.Itoa(cpu)
			edges = append(edges, relationshipEdge{"has_affinity", vcpuName, cpuName})
		}
	}
	emitRelationshipEdges(ch, promLabels, edges)
	logger.Debug("vCPU topology metrics emitted", "domain", domain.domainName, "metrics", emitted)
	return nil, false
}

func lookupStoragePoolName(l *libvirt.Libvirt, path string) string {
	if path == "" {
		return ""
	}
	vol, err := l.StorageVolLookupByPath(path)
	if err != nil {
		return ""
	}
	return vol.Pool
}

// ParseGuestFilesystemFromParams parses virDomainGetGuestInfo filesystem typed params.
func ParseGuestFilesystemFromParams(params []libvirt.TypedParam) []guestFilesystem {
	paramMap := make(map[string]string, len(params))
	for _, p := range params {
		paramMap[p.Field] = typedParamAsString(p)
	}

	count, _ := strconv.Atoi(paramMap[libvirt.DomainGuestInfoFsCount])
	if count == 0 {
		return nil
	}

	var out []guestFilesystem
	for i := 0; i < count; i++ {
		base := fmt.Sprintf("%s%d", libvirt.DomainGuestInfoFsPrefix, i)
		fs := guestFilesystem{
			mountpoint: paramMap[base+libvirt.DomainGuestInfoFsSuffixMountpoint],
			fstype:     paramMap[base+libvirt.DomainGuestInfoFsSuffixFstype],
			device:     paramMap[base+libvirt.DomainGuestInfoFsSuffixName],
		}
		fs.totalBytes = parseUint64(paramMap[base+libvirt.DomainGuestInfoFsSuffixTotalBytes])
		fs.usedBytes = parseUint64(paramMap[base+libvirt.DomainGuestInfoFsSuffixUsedBytes])
		if fs.totalBytes > fs.usedBytes {
			fs.freeBytes = fs.totalBytes - fs.usedBytes
		}
		// Parse disk alias and partition device for topology relationships
		fs.diskAlias = paramMap[base+".disk.0.alias"]
		fs.partitionDevice = paramMap[base+".disk.0.device"]
		if fs.mountpoint != "" || fs.device != "" {
			out = append(out, fs)
		}
	}
	return out
}

func parseGuestFilesystemParams(params []libvirt.TypedParam) []guestFilesystem {
	return ParseGuestFilesystemFromParams(params)
}

// ParseVcpuTopology combines vcpu info and cpumap bytes into topology entries.
func ParseVcpuTopology(info []libvirt.VcpuInfo, cpumaps []byte, maplen, hostCPUs int) []vcpuTopologyEntry {
	if hostCPUs <= 0 {
		hostCPUs = maplen * 8
	}
	var out []vcpuTopologyEntry
	for idx, vcpu := range info {
		entry := vcpuTopologyEntry{
			vcpu:           vcpu.Number,
			currentHostCPU: vcpu.CPU,
			state:          vcpu.State,
			affinity:       parseCpumap(cpumaps, maplen, idx, hostCPUs),
		}
		entry.pinned = isVcpuPinned(entry.affinity, hostCPUs)
		out = append(out, entry)
	}
	return out
}

func parseCpumap(cpumaps []byte, maplen, vcpuIdx, maxCPUs int) []int {
	if maplen <= 0 || maxCPUs <= 0 || len(cpumaps) < (vcpuIdx+1)*maplen {
		return nil
	}
	start := vcpuIdx * maplen
	effectiveBytes := maplen
	if limit := (maxCPUs + 7) / 8; limit < effectiveBytes {
		effectiveBytes = limit
	}
	var cpus []int
	for byteIdx := 0; byteIdx < effectiveBytes; byteIdx++ {
		b := cpumaps[start+byteIdx]
		for bit := 0; bit < 8; bit++ {
			cpu := byteIdx*8 + bit
			if cpu >= maxCPUs {
				return cpus
			}
			if b&(1<<bit) != 0 {
				cpus = append(cpus, cpu)
			}
		}
	}
	return cpus
}

func isVcpuPinned(affinity []int, hostCPUs int) bool {
	return len(affinity) > 0 && len(affinity) < hostCPUs
}

func vcpuStateLabel(state int32) string {
	switch state {
	case 0:
		return "offline"
	case 1:
		return "running"
	case 2:
		return "blocked"
	case 3:
		return "idle"
	default:
		return fmt.Sprintf("unknown_%d", state)
	}
}

func typedParamAsString(p libvirt.TypedParam) string {
	if p.Value.I == nil {
		return ""
	}
	switch v := p.Value.I.(type) {
	case string:
		return v
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	default:
		return fmt.Sprint(v)
	}
}

func parseUint64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func cachedMounts() []mountEntry {
	mountCacheMu.Lock()
	defer mountCacheMu.Unlock()
	if time.Since(mountCacheTime) < mountCacheTTL && len(mountCache) > 0 {
		return mountCache
	}
	mountCache = readMountEntries()
	mountCacheTime = time.Now()
	return mountCache
}

func collectTopologyMetrics(ch chan<- prometheus.Metric, l *libvirt.Libvirt, domain domainMeta, promLabels []string, logger *slog.Logger, timeout time.Duration, collectors []collectFunc) {
	var topologyTimedOut atomic.Bool
	for _, collectFunc := range collectors {
		err, timedOut := collectFunc(ch, l, domain, promLabels, logger, timeout)
		if timedOut {
			topologyTimedOut.Store(true)
			logger.Debug("topology collector timed out", "domain", domain.domainName)
		}
		if err != nil {
			logger.Debug("topology collector failed", "domain", domain.domainName, "msg", err)
		}
	}
	if topologyTimedOut.Load() {
		ch <- prometheus.MustNewConstMetric(libvirtDomainTopologyTimedOutDesc, prometheus.GaugeValue, 1, promLabels...)
	} else {
		ch <- prometheus.MustNewConstMetric(libvirtDomainTopologyTimedOutDesc, prometheus.GaugeValue, 0, promLabels...)
	}
}

// describeTopologyMetrics registers topology metric descriptors.
func describeTopologyMetrics(ch chan<- *prometheus.Desc) {
	ch <- libvirtDomainRelationshipInfoDesc
	ch <- libvirtDomainStorageTopologyInfoDesc
	ch <- libvirtNetworkTopologyInfoDesc
	ch <- libvirtDomainVcpuTopologyInfoDesc
	ch <- libvirtGuestFilesystemInfoDesc
	ch <- libvirtGuestFilesystemSizeBytesDesc
	ch <- libvirtGuestFilesystemUsedBytesDesc
	ch <- libvirtGuestFilesystemFreeBytesDesc
	ch <- libvirtDomainTopologyTimedOutDesc
}

func rwLibvirtURI(uri string) string {
	if strings.HasSuffix(uri, "-ro") {
		return strings.TrimSuffix(uri, "-ro")
	}
	return ""
}
