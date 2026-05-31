# Topology-Aware Libvirt Exporter - Implementation Guide

## Overview

This document describes the transformation of prometheus-libvirt-exporter from a simple monitoring tool into a **topology-aware infrastructure exporter** that exposes complete VM-to-hardware relationships. All metrics are designed for direct graph representation in Neo4j.

## Architecture Principles

- **Topology First**: Every metric exposes relationships between entities
- **Graph Native**: Metrics follow Neo4j naming conventions and can be directly parsed into graph nodes and edges
- **Non-Breaking**: All new metrics are additive; existing functionality is preserved
- **Concurrent**: Collection follows existing concurrency patterns with timeout protection
- **Source Documented**: Each metric indicates its Linux/libvirt data source

## Metrics Structure

All topology metrics follow the pattern:
```
libvirt_domain_<relationship>_<info|info>
  Labels: [domain, <specific-labels>]
  Value: 1 (presence metric)
```

## GAP 1: VM → Host CPU Mapping

### Purpose
Expose which host CPUs vCPUs are pinned to, enabling CPU-aware workload placement analysis.

### Metrics

#### `libvirt_domain_vcpu_host_cpu_info`
- **Labels**: `domain`, `vcpu`, `host_cpu`
- **Value**: 1 when vCPU is pinned; 0 when not pinned
- **Type**: Gauge
- **Source**: `virDomainGetVcpuPinInfo()` + `/proc/[pid]/status` Cpus_allowed_list
- **Example**:
  ```
  libvirt_domain_vcpu_host_cpu_info{domain="vm-prod",vcpu="0",host_cpu="3"} 1
  libvirt_domain_vcpu_host_cpu_info{domain="vm-prod",vcpu="1",host_cpu="4"} 1
  ```
- **Neo4j Graph**:
  ```
  (:VM {name: "vm-prod"})-[:USES_CPU]->(:CPU {id: "3"})
  (:VM {name: "vm-prod"})-[:USES_CPU]->(:CPU {id: "4"})
  ```

#### `libvirt_domain_vcpu_pinned_info`
- **Labels**: `domain`, `vcpu`, `pinned`
- **Value**: 1 if pinned, 0 if unpinned
- **Type**: Gauge
- **Source**: libvirt CPU affinity data
- **Example**:
  ```
  libvirt_domain_vcpu_pinned_info{domain="vm-prod",vcpu="0",pinned="true"} 1
  libvirt_domain_vcpu_pinned_info{domain="vm-prod",vcpu="1",pinned="false"} 0
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainVCPUTopology()`

**Algorithm**:
1. Call `ConnectGetAllDomainStats(domain, DomainStatsVCPU)`
2. Extract `vcpu.maximum` to get vCPU count
3. For each vCPU, emit host CPU mapping
4. Note: Full pinning info requires libvirt >= 1.2.8 with `virDomainGetVcpuPinInfo()`

**Current Limitations**:
- `go-libvirt` doesn't expose `virDomainGetVcpuPinInfo()` yet
- Emits placeholder metrics; requires binding layer extension for production pinning detection

---

## GAP 2: VM → NUMA Mapping

### Purpose
Expose NUMA node allocation for memory-aware placement and topology-driven scheduling.

### Metrics

#### `libvirt_domain_numa_node_info`
- **Labels**: `domain`, `numa_node`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: Domain XML `<numatune><memory nodeset="..."/>`
- **Example**:
  ```
  libvirt_domain_numa_node_info{domain="vm-prod",numa_node="0"} 1
  libvirt_domain_numa_node_info{domain="vm-prod",numa_node="1"} 1
  ```
- **Neo4j Graph**:
  ```
  (:VM {name: "vm-prod"})-[:LOCATED_ON]->(:NUMANode {id: "0"})
  (:VM {name: "vm-prod"})-[:LOCATED_ON]->(:NUMANode {id: "1"})
  ```

#### `libvirt_domain_numa_mode_info`
- **Labels**: `domain`, `numa_mode`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: Domain XML `<numatune><memory mode="..."/>`
- **Modes**: `strict`, `preferred`, `interleave`, `restrictive`
- **Example**:
  ```
  libvirt_domain_numa_mode_info{domain="vm-prod",numa_mode="strict"} 1
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainNUMATopology()`

**Algorithm**:
1. Retrieve domain XML via `DomainGetXMLDesc()`
2. Parse `<numatune>` element for mode attribute
3. Extract `nodeset` attribute and parse CPU list format
4. Emit metrics for each allocated NUMA node

**Helper**: `topology.go` - `ParseNUMATopologyFromXML()`
- Uses regex to extract mode and nodeset
- Calls `parseCPUList()` to expand range notation (e.g., "0-3" → [0,1,2,3])

---

## GAP 3: VM Network Topology

### Purpose
Expose full network chain from VM vNIC → TAP → Linux Bridge → Physical NIC, enabling network topology visualization.

### Metrics

#### `libvirt_domain_network_topology_info`
- **Labels**: `domain`, `tap_device`, `bridge`, `host_nic`, `mac_address`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: `/sys/class/net` symlinks + libvirt interface metadata
- **Example**:
  ```
  libvirt_domain_network_topology_info{
    domain="vm-prod",
    tap_device="vnet4",
    bridge="virbr0",
    host_nic="eno1",
    mac_address="52:54:00:12:34:56"
  } 1
  ```
- **Neo4j Graph**:
  ```
  (:VM {name: "vm-prod"})-[:CONNECTED_TO]->(:Bridge {name: "virbr0"})
  (:Bridge {name: "virbr0"})-[:ATTACHED_TO]->(:NIC {name: "eno1"})
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainNetworkTopology()`

**Algorithm**:
1. Iterate domain interfaces from XML
2. For each interface TAP device (e.g., "vnet4"), call `GetNetworkTopology()`
3. Resolve bridge by reading `/sys/class/net/vnet4/brport/bridge` symlink
4. Find physical NIC by iterating `/sys/class/net/<bridge>/brif/`
5. Emit topology metric

**Helper**: `topology.go` - `GetNetworkTopology()`
- Reads `/sys/class/net/<tap>/brport/bridge` symlink
- Extracts bridge name from symlink target
- Filters `/sys/class/net/<bridge>/brif/` entries to find non-TAP NIC
- Returns `NetworkTopology{TAP, Bridge, HostNIC, MACAddr}`

---

## GAP 4: VM Disk → Host Disk Mapping

### Purpose
Expose complete storage chain from VM disk image → filesystem → partition → physical device, enabling storage topology analysis.

### Metrics

#### `libvirt_domain_disk_topology_info`
- **Labels**: `domain`, `image`, `filesystem`, `partition`, `host_disk`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: `/proc/self/mountinfo`, `/sys/class/block`, libvirt block info
- **Example**:
  ```
  libvirt_domain_disk_topology_info{
    domain="vm-prod",
    image="/var/lib/libvirt/images/vm-prod.qcow2",
    filesystem="ext4",
    partition="nvme0n1p2",
    host_disk="nvme0n1"
  } 1
  ```
- **Neo4j Graph**:
  ```
  (:VM {name: "vm-prod"})-[:USES_DISK]->(:Disk {path: "/var/lib/libvirt/images/vm-prod.qcow2"})
  (:Disk {path: "/var/lib/libvirt/images/vm-prod.qcow2"})-[:STORED_ON]->(:PhysicalDisk {name: "nvme0n1"})
  (:PhysicalDisk {name: "nvme0n1"})-[:HAS_PARTITION]->(:Partition {name: "nvme0n1p2"})
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainDiskTopology()`

**Algorithm**:
1. Iterate domain disks from XML (skip CDROM, FD devices)
2. For each disk source file, call `GetDiskTopology()`
3. Extract physical disk name from partition device
4. Emit topology metric

**Helper**: `topology.go` - `GetDiskTopology()`
- Reads `/proc/self/mountinfo` to find mount for disk image
- Parses mount info: `ID parent device mountpoint fstype ...`
- Finds best matching mountpoint for image file path
- Returns device and filesystem type

**Helper**: `getPhysicalDisk()`
- Uses regex to remove partition suffix: `nvme0n1p2` → `nvme0n1`
- Pattern: `/p?\d+$/` (removes optional 'p' and trailing digits)

**Helper**: `FindBlockDeviceInSysfs()`
- Verifies device exists in `/sys/class/block/<device>`
- Enables confidence scoring in topology discovery

---

## GAP 5: Guest Filesystem Topology

### Purpose
Expose filesystems visible *inside* the VM when QEMU guest agent is available, enabling guest-level storage topology.

### Metrics

#### `libvirt_domain_guest_filesystem_info`
- **Labels**: `domain`, `device`, `mountpoint`, `fstype`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: `virDomainGetGuestInfo(domain, VIR_DOMAIN_GUEST_INFO_DISKS)` - requires QEMU guest agent
- **Example**:
  ```
  libvirt_domain_guest_filesystem_info{
    domain="vm-prod",
    device="/dev/sda1",
    mountpoint="/",
    fstype="ext4"
  } 1
  ```

#### `libvirt_domain_guest_filesystem_size_bytes`
- **Labels**: `domain`, `device`
- **Value**: Total filesystem capacity in bytes
- **Type**: Gauge
- **Source**: QEMU guest agent fsinfo
- **Example**:
  ```
  libvirt_domain_guest_filesystem_size_bytes{domain="vm-prod",device="/dev/sda1"} 21474836480
  ```

#### `libvirt_domain_guest_filesystem_used_bytes`
- **Labels**: `domain`, `device`
- **Value**: Used space in bytes
- **Type**: Gauge
- **Source**: QEMU guest agent fsinfo
- **Example**:
  ```
  libvirt_domain_guest_filesystem_used_bytes{domain="vm-prod",device="/dev/sda1"} 10737418240
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainGuestFilesystem()`

**Current Status**: Placeholder implementation
- Requires `go-libvirt` binding for `virDomainGetGuestInfo()`
- Binding doesn't exist yet in go-libvirt library
- When available, implementation will:
  1. Check if guest agent is available (VIR_DOMAIN_GUEST_INFO_DISKS)
  2. Call `virDomainGetGuestInfo()` to retrieve guest filesystem info
  3. Parse returned `virDomainGuestInfo` structure
  4. Extract device, mountpoint, fstype, capacity, usage
  5. Emit metrics

**Production Deployment**:
1. Ensure QEMU guest agent is running in VM:
   ```bash
   apt-get install qemu-guest-agent  # Debian/Ubuntu
   yum install qemu-guest-agent       # RHEL/CentOS
   systemctl start qemu-guest-agent
   ```
2. Enable guest agent in domain XML:
   ```xml
   <channel type="unix">
     <target type="virtio" name="com.redhat.spice.0"/>
   </channel>
   ```
3. Once go-libvirt supports `virDomainGetGuestInfo()`, this collector will emit metrics automatically

---

## GAP 6: VM Relationship Metrics

### Purpose
Emit generic relationship metrics for direct Neo4j graph building without query translation.

### Metrics

#### `libvirt_domain_relationship_info`
- **Labels**: `domain`, `relation`, `target`
- **Value**: 1 (presence metric)
- **Type**: Gauge
- **Source**: Domain XML + libvirt metadata
- **Supported Relations**:
  - `attached_disk`: VM has disk attached (target = device name)
  - `attached_interface`: VM has network interface (target = device name)
  - `storage_pool`: VM uses storage pool (target = pool name)
- **Example**:
  ```
  libvirt_domain_relationship_info{domain="vm-prod",relation="attached_disk",target="vda"} 1
  libvirt_domain_relationship_info{domain="vm-prod",relation="attached_interface",target="vnet4"} 1
  libvirt_domain_relationship_info{domain="vm-prod",relation="storage_pool",target="default"} 1
  ```

### Implementation Details

**File**: `pkg/exporter/prometheus-libvirt-exporter.go` - `CollectDomainRelationships()`

**Algorithm**:
1. For each disk in domain XML:
   - Emit `attached_disk` relation metric with target = disk device
2. For each interface in domain XML:
   - Emit `attached_interface` relation metric with target = interface device
3. Emit `storage_pool` relation metric (currently hardcoded to "default")

**Future Extensions**:
- Add `memory_allocation` relation
- Add `cpu_allocation` relation with vCPU count
- Add `numa_affinity` relation
- Add relation strength metrics (frequency of access)

---

## Neo4j Integration

All metrics can be parsed directly into Neo4j graph:

### Graph Schema

```cypher
// Create indices for fast lookups
CREATE INDEX idx_vm_name ON (v:VM) (name);
CREATE INDEX idx_cpu_id ON (c:CPU) (id);
CREATE INDEX idx_disk_path ON (d:Disk) (path);
CREATE INDEX idx_bridge_name ON (b:Bridge) (name);
CREATE INDEX idx_nic_name ON (n:NIC) (name);
CREATE INDEX idx_numa_id ON (n:NUMANode) (id);

// Example relationships
MATCH (vm:VM {name: "vm-prod"})
MATCH (cpu:CPU {id: "3"})
CREATE (vm)-[:USES_CPU {metric: "libvirt_domain_vcpu_host_cpu_info"}]->(cpu);

MATCH (vm:VM {name: "vm-prod"})
MATCH (disk:Disk {path: "/var/lib/libvirt/images/vm-prod.qcow2"})
CREATE (vm)-[:USES_DISK {metric: "libvirt_domain_disk_topology_info"}]->(disk);

MATCH (disk:Disk)
MATCH (pd:PhysicalDisk {name: "nvme0n1"})
CREATE (disk)-[:STORED_ON {metric: "libvirt_domain_disk_topology_info"}]->(pd);
```

### Querying Examples

```cypher
// Find all VMs using specific CPU
MATCH (cpu:CPU {id: "0"})<-[:USES_CPU]-(vm:VM)
RETURN vm.name;

// Find all storage attached to VM
MATCH (vm:VM {name: "vm-prod"})-[:USES_DISK]->(disk:Disk)-[:STORED_ON]->(pd:PhysicalDisk)
RETURN disk.path, pd.name;

// Find network topology for VM
MATCH (vm:VM {name: "vm-prod"})-[:CONNECTED_TO]->(bridge:Bridge)-[:ATTACHED_TO]->(nic:NIC)
RETURN bridge.name, nic.name;

// Find VMs in specific NUMA node
MATCH (vm:VM)-[:LOCATED_ON]->(numa:NUMANode {id: "0"})
RETURN vm.name;
```

---

## Testing

Unit tests are provided in `prometheus-libvirt-exporter_test.go`:

- `TestParseCPUList`: Validates CPU list parsing (ranges, single values, mixed)
- `TestGetPhysicalDisk`: Validates partition-to-disk extraction
- `TestParseNUMATopologyFromXML`: Validates NUMA XML parsing
- `TestIsFileOnDevice`: Validates file-to-mountpoint matching

Run tests:
```bash
go test ./pkg/exporter -v
```

---

## Troubleshooting

### Metrics Not Appearing

1. **vCPU metrics empty**: Requires domain XML parsing; ensure libvirt can access domain XML
2. **NUMA metrics missing**: Check domain has `<numatune>` element in XML
3. **Network topology incomplete**: Verify `/sys/class/net` is accessible and bridge exists
4. **Disk topology incomplete**: Check `/proc/self/mountinfo` readable and disk mounted

### Performance Considerations

- All collectors use timeout protection (configurable via `--exporter.timeout`)
- Concurrent collection respects `--exporter.max-concurrent-collects`
- NUMA/network topology resolution adds ~10-50ms per domain (sysfs I/O bound)
- Disk topology resolution adds ~5-20ms per domain (mountinfo parsing)

### Limitations

- **vCPU pinning**: Requires libvirt API extension in go-libvirt
- **Guest agent data**: Requires QEMU guest agent + libvirt binding
- **Storage pool membership**: Currently hardcoded to "default"
- **Cross-numa memory**: Not tracked per NUMA node allocation size

---

## Migration Guide

For existing exporter users:

1. **Zero breaking changes**: All new metrics are additive
2. **No config changes required**: Topology collection enabled by default
3. **Scrape interval unchanged**: Use existing settings
4. **Alert rules**: Existing alerting rules continue to function
5. **Grafana dashboards**: Existing panels unaffected

Optional: Import new dashboard using metrics with `_topology_` prefix for topology visualization.

---

## Contributing

To add new topology relationships:

1. Define metric descriptor with `##` comment
2. Implement collector function following existing pattern
3. Add to `collectFunc` list in `CollectDomain()`
4. Add descriptor to `Describe()` method
5. Add unit tests to `prometheus-libvirt-exporter_test.go`
6. Document Neo4j mapping in this guide
