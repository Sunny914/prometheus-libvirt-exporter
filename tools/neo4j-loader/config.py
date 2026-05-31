"""Configuration for the Neo4j topology loader."""

import os

EXPORTER_URL = os.environ.get(
    "EXPORTER_URL",
    "http://localhost:9177/metrics",
)

NEO4J_URI = os.environ.get("NEO4J_URI", "bolt://localhost:7687")
NEO4J_USER = os.environ.get("NEO4J_USER", "neo4j")
NEO4J_PASSWORD = os.environ.get("NEO4J_PASSWORD", "password")

DISCOVERED_BY = os.environ.get(
    "DISCOVERED_BY",
    "prometheus-libvirt-exporter",
)

REQUEST_TIMEOUT_SECONDS = int(os.environ.get("REQUEST_TIMEOUT_SECONDS", "30"))
