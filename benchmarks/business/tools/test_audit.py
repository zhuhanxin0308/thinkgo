"""审计必须区分业务损坏、客户端未知提交和正常排空。"""

import unittest

from audit import assess


class AuditTest(unittest.TestCase):
    def record(self):
        return {"orders": 10, "audit_rows": 10, "new_comments": 4, "pending_orders": 0,
                "missing_audits": 0, "invalid_totals": 0, "negative_stock": 0,
                "inventory_mismatches": 0, "ticket_version_mismatches": 0, "cross_tenant_items": 0}

    def test_zero_invariant_failures_and_confirmed_commits_are_required(self):
        result = assess(self.record(), 10, 4)
        self.assertTrue(result["valid"])
        self.assertEqual(result["unconfirmed_orders"], 0)
        for field in ["pending_orders", "missing_audits", "invalid_totals", "negative_stock",
                      "inventory_mismatches", "ticket_version_mismatches", "cross_tenant_items"]:
            damaged = self.record()
            damaged[field] = 1
            self.assertFalse(assess(damaged, 10, 4)["valid"], field)

    def test_unknown_commit_is_preserved_and_missing_commit_is_invalid(self):
        self.assertEqual(assess(self.record(), 8, 3)["unconfirmed_orders"], 2)
        self.assertEqual(assess(self.record(), 8, 3)["unconfirmed_comments"], 1)
        self.assertFalse(assess(self.record(), 11, 4)["valid"])
        self.assertFalse(assess(self.record(), 10, 5)["valid"])
        with self.assertRaises(ValueError):
            assess(self.record(), -1, 4)


if __name__ == "__main__":
    unittest.main()
