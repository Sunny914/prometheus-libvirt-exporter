"""Neo4j client for topology graph MERGE operations."""

from __future__ import annotations

import re
from dataclasses import dataclass

from neo4j import GraphDatabase

REL_TYPE_PATTERN = re.compile(r"^[A-Z][A-Z0-9_]*$")
DISK_PATTERN = re.compile(r"^(nvme\d+n\d+(p\d+)?|sd[a-z]\d*|vd[a-z]\d*)$")
VCPU_PATTERN = re.compile(r"^vcpu\d+$")
CPU_PATTERN = re.compile(r"^cpu\d+$")
PARTITION_PATTERN = re.compile(r"^(nvme\d+n\d+p\d+|sd[a-z]\d+|vd[a-z]\d+)$")


@dataclass
class LoadStats:
    nodes_touched: int
    relationships_merged: int


def infer_node_label(name: str, rel: dict) -> str:
    """Infer an additional Neo4j label for visualization."""
    if name == rel["domain"]:
        return "VM"
    if VCPU_PATTERN.fullmatch(name):
        return "VCPU"
    if CPU_PATTERN.fullmatch(name):
        return "CPU"
    if PARTITION_PATTERN.fullmatch(name):
        return "Partition"
    if rel["rel_cypher"] == "VCPU_HOST_CPU" and name != rel["domain"]:
        return "CPU"
    if name.startswith("vnet"):
        return "NetworkInterface"
    if name.startswith("virbr"):
        return "Bridge"
    if name.endswith(".qcow2"):
        return "DiskImage"
    if name.startswith("/"):
        return "Filesystem"
    if DISK_PATTERN.fullmatch(name):
        return "Disk"
    return "Resource"


class Neo4jTopologyClient:
    """Create typed nodes and relationships with MERGE (idempotent)."""

    def __init__(
        self,
        uri: str,
        user: str,
        password: str,
        discovered_by: str,
    ) -> None:
        self._driver = GraphDatabase.driver(uri, auth=(user, password))
        self._discovered_by = discovered_by

    def close(self) -> None:
        self._driver.close()

    def verify_connectivity(self) -> None:
        self._driver.verify_connectivity()

    @staticmethod
    def _validate_rel_cypher(rel_cypher: str) -> str:
        if not REL_TYPE_PATTERN.fullmatch(rel_cypher):
            raise ValueError(f"unsafe relationship type for Cypher: {rel_cypher!r}")
        return rel_cypher

    def merge_topology(self, relationships: list[dict]) -> LoadStats:
        """
        MERGE nodes and edges for all relationships.

        Returns counts of unique node names touched and relationships merged.
        """
        node_names: set[str] = set()
        rel_count = 0

        with self._driver.session() as session:
            for rel in relationships:
                source = rel["source"]
                target = rel["target"]
                rel_cypher = self._validate_rel_cypher(rel["rel_cypher"])
                source_label = infer_node_label(source, rel)
                target_label = infer_node_label(target, rel)
                node_names.add(source)
                node_names.add(target)

                query = f"""
                MERGE (a:{source_label} {{name: $source}})
                ON CREATE SET a.discovered_by = $discovered_by
                ON MATCH SET a.discovered_by = $discovered_by
                MERGE (b:{target_label} {{name: $target}})
                ON CREATE SET b.discovered_by = $discovered_by
                ON MATCH SET b.discovered_by = $discovered_by
                MERGE (a)-[r:{rel_cypher}]->(b)
                ON CREATE SET r.domain = $domain, r.relation_type = $relation_type
                ON MATCH SET r.domain = $domain, r.relation_type = $relation_type
                """
                session.run(
                    query,
                    source=source,
                    target=target,
                    domain=rel["domain"],
                    relation_type=rel["relation_type"],
                    discovered_by=self._discovered_by,
                )
                rel_count += 1

        return LoadStats(
            nodes_touched=len(node_names),
            relationships_merged=rel_count,
        )

    def count_graph(self) -> tuple[int, int]:
        """Return (node_count, relationship_count) in the database."""
        with self._driver.session() as session:
            nodes = session.run("MATCH (n) RETURN count(n) AS c").single()["c"]
            rels = session.run("MATCH ()-[r]->() RETURN count(r) AS c").single()["c"]
        return int(nodes), int(rels)
