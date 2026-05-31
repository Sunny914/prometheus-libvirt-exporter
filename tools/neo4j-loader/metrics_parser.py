"""Parse libvirt_domain_relationship_info metrics from Prometheus text exposition."""

from __future__ import annotations

import re
import sys
from typing import Any

from prometheus_client.parser import text_string_to_metric_families

METRIC_NAME = "libvirt_domain_relationship_info"
REQUIRED_LABELS = ("domain", "relation_type", "source", "target")

# Fallback for hand-written samples that use "relation" instead of "relation_type".
RELATION_LABEL_ALIASES = ("relation_type", "relation")


def relation_type_to_cypher(relation_type: str) -> str:
    """Convert attached_disk to ATTACHED_DISK for Neo4j relationship types."""
    if not relation_type or not re.fullmatch(r"[a-z][a-z0-9_]*", relation_type):
        raise ValueError(f"invalid relation_type: {relation_type!r}")
    return relation_type.upper()


def _normalize_labels(labels: dict[str, str]) -> dict[str, str] | None:
    """Map sample labels to required keys; return None if incomplete."""
    out = dict(labels)
    if "relation_type" not in out:
        for alias in RELATION_LABEL_ALIASES:
            if alias in out and alias != "relation_type":
                out["relation_type"] = out[alias]
                break
    missing = [k for k in REQUIRED_LABELS if k not in out or not str(out[k]).strip()]
    if missing:
        return None
    return {k: str(out[k]).strip() for k in REQUIRED_LABELS}


def parse_relationship_metrics(metrics_text: str) -> list[dict[str, Any]]:
    """
    Parse exporter metrics text and return relationship dicts.

    Each dict contains: domain, relation_type, source, target, and rel_cypher
    (Neo4j relationship type name).
    """
    relationships: list[dict[str, Any]] = []
    warnings = 0

    for family in text_string_to_metric_families(metrics_text):
        if family.name != METRIC_NAME:
            continue
        for sample in family.samples:
            if sample.name != METRIC_NAME:
                continue
            labels = _normalize_labels(sample.labels)
            if labels is None:
                warnings += 1
                print(
                    f"warning: skipping malformed {METRIC_NAME} sample "
                    f"(labels={sample.labels!r})",
                    file=sys.stderr,
                )
                continue
            try:
                rel_cypher = relation_type_to_cypher(labels["relation_type"])
            except ValueError as exc:
                warnings += 1
                print(f"warning: {exc}; labels={sample.labels!r}", file=sys.stderr)
                continue
            relationships.append({**labels, "rel_cypher": rel_cypher})

    if warnings:
        print(f"warning: skipped {warnings} malformed sample(s)", file=sys.stderr)

    return relationships
