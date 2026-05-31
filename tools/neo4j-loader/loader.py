#!/usr/bin/env python3
"""Sync libvirt topology relationship metrics into Neo4j."""

from __future__ import annotations

import sys

import requests

import config
from metrics_parser import parse_relationship_metrics
from neo4j_client import Neo4jTopologyClient


def fetch_metrics(url: str, timeout: int) -> str:
    response = requests.get(url, timeout=timeout)
    response.raise_for_status()
    return response.text


def main() -> int:
    print(f"Fetching metrics from {config.EXPORTER_URL}")
    try:
        metrics_text = fetch_metrics(
            config.EXPORTER_URL,
            config.REQUEST_TIMEOUT_SECONDS,
        )
    except requests.RequestException as exc:
        print(f"error: failed to fetch metrics: {exc}", file=sys.stderr)
        return 1

    relationships = parse_relationship_metrics(metrics_text)
    print(f"Found {len(relationships)} topology relationships")

    if not relationships:
        print(
            "warning: no libvirt_domain_relationship_info metrics found; "
            "nothing to load",
            file=sys.stderr,
        )
        return 0

    client = Neo4jTopologyClient(
        uri=config.NEO4J_URI,
        user=config.NEO4J_USER,
        password=config.NEO4J_PASSWORD,
        discovered_by=config.DISCOVERED_BY,
    )
    try:
        client.verify_connectivity()
        print("Connected to Neo4j")

        stats = client.merge_topology(relationships)
        nodes, rels = client.count_graph()

        print(f"Created/Updated {stats.nodes_touched} nodes")
        print(f"Created/Updated {stats.relationships_merged} relationships")
        print(f"Graph totals: {nodes} Resource nodes, {rels} relationships")
        print("Topology graph successfully synchronized")
    except Exception as exc:
        print(f"error: Neo4j load failed: {exc}", file=sys.stderr)
        return 1
    finally:
        client.close()

    return 0


if __name__ == "__main__":
    sys.exit(main())
