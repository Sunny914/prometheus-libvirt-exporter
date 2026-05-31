"""Unit tests for metrics_parser (run with: python -m unittest test_metrics_parser.py)."""

import unittest
from pathlib import Path

from metrics_parser import parse_relationship_metrics, relation_type_to_cypher

SAMPLE = Path(__file__).parent / "examples" / "sample_metrics.txt"


class TestMetricsParser(unittest.TestCase):
    def test_relation_type_to_cypher(self):
        self.assertEqual(relation_type_to_cypher("attached_disk"), "ATTACHED_DISK")
        self.assertEqual(relation_type_to_cypher("bridge_uplink"), "BRIDGE_UPLINK")

    def test_parse_sample_file(self):
        text = SAMPLE.read_text(encoding="utf-8")
        rels = parse_relationship_metrics(text)
        self.assertEqual(len(rels), 10)
        self.assertEqual(rels[0]["relation_type"], "attached_disk")
        self.assertEqual(rels[0]["rel_cypher"], "ATTACHED_DISK")
        self.assertEqual(rels[0]["source"], "test-cvm")
        self.assertEqual(rels[0]["target"], "vda")

    def test_skips_malformed(self):
        text = """
# TYPE libvirt_domain_relationship_info gauge
libvirt_domain_relationship_info{domain="x",source="a"} 1
libvirt_domain_relationship_info{domain="x",relation_type="attached_disk",source="a",target="b"} 1
"""
        rels = parse_relationship_metrics(text)
        self.assertEqual(len(rels), 1)


if __name__ == "__main__":
    unittest.main()
