#!/usr/bin/env python3
"""End-to-end validation for exporter → loader → Neo4j pipeline."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import requests
from neo4j import GraphDatabase

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

import config  # noqa: E402


def check(name: str, ok: bool, detail: str = "") -> bool:
    status = "PASS" if ok else "FAIL"
    line = f"[{status}] {name}"
    if detail:
        line += f" — {detail}"
    print(line)
    return ok


def main() -> int:
    passed = True

    # 1. Exporter reachable
    try:
        r = requests.get(config.EXPORTER_URL, timeout=10)
        passed &= check("Exporter reachable", r.status_code == 200, str(r.status_code))
        body = r.text
    except requests.RequestException as exc:
        passed &= check("Exporter reachable", False, str(exc))
        body = ""

    # 2. Relationship metrics found
    count = body.count("libvirt_domain_relationship_info{")
    passed &= check(
        "Relationship metrics found",
        count > 0,
        f"{count} sample(s)" if count else "none",
    )

    # 3. Neo4j container / connectivity
    docker_ok = False
    try:
        out = subprocess.run(
            ["docker", "ps", "--format", "{{.Names}}"],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        docker_ok = "neo4j" in out.stdout.lower() or out.returncode == 0
        passed &= check(
            "Docker available",
            out.returncode == 0,
            out.stdout.strip()[:80] or "docker ps",
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        passed &= check("Docker available", False, str(exc))

    try:
        driver = GraphDatabase.driver(
            config.NEO4J_URI,
            auth=(config.NEO4J_USER, config.NEO4J_PASSWORD),
        )
        driver.verify_connectivity()
        driver.close()
        passed &= check("Neo4j reachable", True, config.NEO4J_URI)
    except Exception as exc:
        passed &= check("Neo4j reachable", False, str(exc))

    # 4. Loader success
    loader = subprocess.run(
        [sys.executable, str(ROOT / "loader.py")],
        cwd=str(ROOT),
        capture_output=True,
        text=True,
        timeout=120,
    )
    print(loader.stdout, end="")
    if loader.stderr:
        print(loader.stderr, file=sys.stderr, end="")
    passed &= check(
        "Loader success",
        loader.returncode == 0,
        f"exit {loader.returncode}",
    )

    # 5. Graph visible
    try:
        driver = GraphDatabase.driver(
            config.NEO4J_URI,
            auth=(config.NEO4J_USER, config.NEO4J_PASSWORD),
        )
        with driver.session() as session:
            nodes = session.run("MATCH (n:Resource) RETURN count(n) AS c").single()["c"]
            rels = session.run("MATCH ()-[r]->() RETURN count(r) AS c").single()["c"]
        driver.close()
        passed &= check(
            "Graph visible",
            nodes > 0 and rels > 0,
            f"{nodes} nodes, {rels} relationships",
        )
    except Exception as exc:
        passed &= check("Graph visible", False, str(exc))

    print()
    if passed:
        print("All validation checks passed.")
        return 0
    print("Some validation checks failed.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
