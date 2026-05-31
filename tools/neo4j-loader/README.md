# Neo4j Topology Loader

Consumes `libvirt_domain_relationship_info` metrics from [prometheus-libvirt-exporter](../../README.md) and builds an idempotent Neo4j graph using `MERGE`.

```
Prometheus Libvirt Exporter
        ↓
Relationship Metrics (HTTP /metrics)
        ↓
Neo4j Loader (this tool)
        ↓
Neo4j Graph (:Resource nodes + typed edges)
```

## Prerequisites

- Python 3.10+
- Docker (for Neo4j)
- Running exporter with topology metrics, e.g. `http://localhost:9177/metrics`

## Install

### Start Neo4j

```bash
cd tools/neo4j-loader
docker compose up -d
# or: docker-compose up -d
```

If Docker Compose is not installed, start Neo4j directly:

```bash
docker run -d --name neo4j \
  -p 7474:7474 -p 7687:7687 \
  -e NEO4J_AUTH=neo4j/password \
  neo4j:latest
```

Verify:

```bash
docker ps
```

Neo4j Browser: http://localhost:7474 (user `neo4j`, password `password`).

### Python dependencies

```bash
cd tools/neo4j-loader
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

## Configuration

Defaults in `config.py` (override with environment variables):

| Variable | Default |
|----------|---------|
| `EXPORTER_URL` | `http://localhost:9177/metrics` |
| `NEO4J_URI` | `bolt://localhost:7687` |
| `NEO4J_USER` | `neo4j` |
| `NEO4J_PASSWORD` | `password` |
| `DISCOVERED_BY` | `prometheus-libvirt-exporter` |
| `REQUEST_TIMEOUT_SECONDS` | `30` |

Example:

```bash
export EXPORTER_URL=http://192.168.1.10:9177/metrics
export NEO4J_PASSWORD=secret
```

## Run loader

```bash
python loader.py
```

Example output:

```
Fetching metrics from http://localhost:9177/metrics
Found 18 topology relationships
Connected to Neo4j
Created/Updated 12 nodes
Created/Updated 18 relationships
Graph totals: 12 Resource nodes, 18 relationships
Topology graph successfully synchronized
```

Re-running the loader is safe: `MERGE` prevents duplicate nodes and edges.

## Data model

**Node**

```cypher
(:Resource {
  name: "<source or target id>",
  discovered_by: "prometheus-libvirt-exporter"
})
```

**Relationship** — type derived from `relation_type` label (`attached_disk` → `ATTACHED_DISK`):

```cypher
(a:Resource)-[:ATTACHED_DISK]->(b:Resource)
```

Edge properties: `domain`, `relation_type`.

## Neo4j visualization (Browser)

Open http://localhost:7474, connect, then run:

### Query 1 — Full graph

```cypher
MATCH (n)-[r]->(m)
RETURN n, r, m
```

### Query 2 — Storage topology

```cypher
MATCH (n)-[r]->(m)
WHERE type(r) IN [
  'ATTACHED_DISK',
  'BACKING_FILE',
  'PHYSICAL_DISK',
  'STORAGE_POOL'
]
RETURN n, r, m
```

### Query 3 — Network topology

```cypher
MATCH (n)-[r]->(m)
WHERE type(r) IN [
  'ATTACHED_INTERFACE',
  'CONNECTED_BRIDGE',
  'BRIDGE_UPLINK'
]
RETURN n, r, m
```

### Query 4 — Filesystem topology

```cypher
MATCH (n)-[r]->(m)
WHERE type(r) = 'GUEST_FILESYSTEM'
RETURN n, r, m
```

### Query 5 — CPU topology

```cypher
MATCH (n)-[r]->(m)
WHERE type(r) = 'VCPU_HOST_CPU'
RETURN n, r, m
```

More queries: [examples/cypher_queries.cypher](examples/cypher_queries.cypher).

## End-to-end validation

Manual checks:

```bash
# 1. Exporter reachable
curl -s http://localhost:9177/metrics | head

# 2. Relationship metrics present
curl -s http://localhost:9177/metrics | grep libvirt_domain_relationship_info

# 3. Neo4j running
docker ps

# 4. Load graph
python loader.py

# 5. Graph in Neo4j (Browser or cypher-shell)
```

Automated validation:

```bash
python examples/validate.py
```

Parser unit tests (offline, uses `examples/sample_metrics.txt`):

```bash
python -m unittest test_metrics_parser.py -v
```

## Dynamic update test

When infrastructure changes, re-run the loader; the graph grows without manual node creation.

1. Start exporter and Neo4j; run `python loader.py` once and note node count in Browser:
   ```cypher
   MATCH (n:Resource) RETURN count(n)
   ```
2. Create a new VM (or start an additional domain) on the libvirt host.
3. Wait for exporter scrape / refresh metrics:
   ```bash
   curl -s http://localhost:9177/metrics | grep libvirt_domain_relationship_info | wc -l
   ```
4. Run `python loader.py` again.
5. Verify new `Resource` nodes and relationships appear for the new domain.

Expected: graph size increases; repeated loads do not duplicate edges.

## Troubleshooting

| Symptom | Cause | Fix |
|---------|--------|-----|
| `Failed to fetch metrics` | Exporter not running or wrong URL | Start exporter; set `EXPORTER_URL` |
| `no libvirt_domain_relationship_info metrics` | Topology collection disabled or timed out | Check `libvirt_domain_topology_timed_out`; see exporter README |
| `Neo4j load failed: ServiceUnavailable` | Neo4j not up | `docker compose up -d`; wait for healthcheck |
| `authentication failure` | Wrong password | Match `NEO4J_PASSWORD` to `NEO4J_AUTH` in compose (`neo4j/password`) |
| Empty graph after load | Metrics file has no relationship lines | `grep libvirt_domain_relationship_info` on `/metrics` |
| `invalid relation_type` warnings | Malformed exporter labels | Fix exporter; parser skips bad samples |

## Project layout

```
tools/neo4j-loader/
├── requirements.txt
├── loader.py              # Main entrypoint
├── neo4j_client.py        # MERGE nodes/edges
├── metrics_parser.py      # Prometheus text → relationships
├── config.py
├── docker-compose.yml
├── test_metrics_parser.py
├── README.md
└── examples/
    ├── cypher_queries.cypher
    ├── sample_metrics.txt
    └── validate.py
```
