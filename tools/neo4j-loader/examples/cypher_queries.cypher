// Query 1: Show full graph
MATCH (n)-[r]->(m)
RETURN n, r, m;

// Query 2: Storage topology
MATCH (n)-[r]->(m)
WHERE type(r) IN [
  'ATTACHED_DISK',
  'BACKING_FILE',
  'PHYSICAL_DISK',
  'STORAGE_POOL'
]
RETURN n, r, m;

// Query 3: Network topology
MATCH (n)-[r]->(m)
WHERE type(r) IN [
  'ATTACHED_INTERFACE',
  'CONNECTED_BRIDGE',
  'BRIDGE_UPLINK'
]
RETURN n, r, m;

// Query 4: Filesystem topology
MATCH (n)-[r]->(m)
WHERE type(r) = 'GUEST_FILESYSTEM'
RETURN n, r, m;

// Query 5: CPU topology
MATCH (n)-[r]->(m)
WHERE type(r) = 'VCPU_HOST_CPU'
RETURN n, r, m;
