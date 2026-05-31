package exporter

import (
	"testing"

	"github.com/digitalocean/go-libvirt"
	"github.com/inovex/prometheus-libvirt-exporter/libvirt_schema"
	"github.com/stretchr/testify/assert"
)

func TestParseGuestFilesystemFromParams(t *testing.T) {
	params := []libvirt.TypedParam{
		{Field: "fs.count", Value: *libvirt.NewTypedParamValueInt(1)},
		{Field: "fs.0.mountpoint", Value: *libvirt.NewTypedParamValueString("/")},
		{Field: "fs.0.name", Value: *libvirt.NewTypedParamValueString("/dev/vda2")},
		{Field: "fs.0.fstype", Value: *libvirt.NewTypedParamValueString("ext4")},
		{Field: "fs.0.total-bytes", Value: *libvirt.NewTypedParamValueUllong(2048)},
		{Field: "fs.0.used-bytes", Value: *libvirt.NewTypedParamValueUllong(1024)},
	}
	fss := ParseGuestFilesystemFromParams(params)
	assert.Len(t, fss, 1)
	assert.Equal(t, "/", fss[0].mountpoint)
	assert.Equal(t, "/dev/vda2", fss[0].device)
	assert.Equal(t, "ext4", fss[0].fstype)
	assert.Equal(t, uint64(2048), fss[0].totalBytes)
	assert.Equal(t, uint64(1024), fss[0].freeBytes)
}

func TestParseGuestFilesystemFromParamsEmpty(t *testing.T) {
	assert.Nil(t, ParseGuestFilesystemFromParams(nil))
	assert.Nil(t, ParseGuestFilesystemFromParams([]libvirt.TypedParam{
		{Field: "fs.count", Value: *libvirt.NewTypedParamValueInt(0)},
	}))
}

func TestBuildDomainRelationshipEdges(t *testing.T) {
	domain := domainMeta{
		domainName: "vm1",
		libvirtSchema: libvirt_schema.Domain{
			Devices: libvirt_schema.Devices{
				Disks: []libvirt_schema.Disk{
					{Device: "disk", Target: libvirt_schema.DiskTarget{Device: "vda"}, Source: libvirt_schema.DiskSource{File: "/var/lib/libvirt/images/vm1.qcow2"}},
					{Device: "cdrom", Target: libvirt_schema.DiskTarget{Device: "hda"}},
				},
				Interfaces: []libvirt_schema.Interface{
					{Target: libvirt_schema.InterfaceTarget{Device: "vnet0"}, Source: libvirt_schema.InterfaceSource{Bridge: "virbr0"}},
				},
			},
		},
	}

	edges := BuildDomainRelationshipEdges(domain)
	assert.Len(t, edges, 3)
	assert.Contains(t, edges, relationshipEdge{"attached_disk", "vm1", "vda"})
	assert.Contains(t, edges, relationshipEdge{"attached_interface", "vm1", "vnet0"})
	assert.Contains(t, edges, relationshipEdge{"connected_bridge", "vnet0", "virbr0"})
}

func TestParseCpumap(t *testing.T) {
	cpumaps := []byte{
		0b00000001,
		0b00001000,
	}
	assert.Equal(t, []int{0}, parseCpumap(cpumaps, 1, 0, 8))
	assert.Equal(t, []int{3}, parseCpumap(cpumaps, 1, 1, 8))
}

func TestParseVcpuTopology(t *testing.T) {
	cpumaps := make([]byte, 32)
	cpumaps[0] = 0b00000001
	for i := 16; i < 32; i++ {
		cpumaps[i] = 0xff
	}
	info := []libvirt.VcpuInfo{
		{Number: 0, CPU: 1, State: 1},
		{Number: 1, CPU: 7, State: 1},
	}
	entries := ParseVcpuTopology(info, cpumaps, 16, 8)
	assert.Len(t, entries, 2)
	assert.True(t, entries[0].pinned)
	assert.False(t, entries[1].pinned)
	assert.Equal(t, []int{0}, entries[0].affinity)
	assert.Len(t, entries[1].affinity, 8)
}

func TestParseNetworkTopologyBridge(t *testing.T) {
	assert.Equal(t, "sda", stripPartitionSuffix("sda1"))
	assert.Equal(t, "nvme0n1", stripPartitionSuffix("nvme0n1p2"))
}

func TestRwLibvirtURI(t *testing.T) {
	assert.Equal(t, "/var/run/libvirt/libvirt-sock", rwLibvirtURI("/var/run/libvirt/libvirt-sock-ro"))
	assert.Equal(t, "", rwLibvirtURI("/var/run/libvirt/libvirt-sock"))
}

func TestIsVcpuPinned(t *testing.T) {
	assert.True(t, isVcpuPinned([]int{1}, 8))
	assert.False(t, isVcpuPinned([]int{0, 1, 2, 3, 4, 5, 6, 7}, 8))
}
