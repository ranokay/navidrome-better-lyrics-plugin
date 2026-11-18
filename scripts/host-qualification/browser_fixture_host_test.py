import unittest

import browser_fixture_host as host


class RewriteKVStoreTest(unittest.TestCase):
    def source(self):
        return (
            "package plugins\n\n"
            'const notExpiredFilter = "(expires_at IS NULL OR expires_at >= datetime(\'now\'))"\n\n'
            "func setValue() {\n"
            "\t_, err := db.Exec(`\n"
            "\t\tINSERT INTO kvstore (key, value, size, created_at, updated_at, expires_at)\n"
            "\t\tVALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, datetime('now', ?))\n"
            "\t\tON CONFLICT(key) DO UPDATE SET value = excluded.value\n"
            "\t`, key, value, size, expiresAt)\n"
            "}\n"
        )

    def test_overlays_ttl_clock_only_when_requested(self):
        source = self.source()
        generated = host.rewrite_kvstore(source, "+25 hours")
        self.assertIn("var notExpiredFilter", generated)
        self.assertIn("qualificationKVClockTTLModifier", generated)
        self.assertIn('const qualificationKVClockOffset = "+25 hours"', generated)
        self.assertIn("datetime('now', ?`", generated)
        self.assertNotEqual(source, generated)
        self.assertIn(host.KV_FILTER, source)
        self.assertNotIn("qualificationKVClockOffset", source)

        neutral = host.rewrite_kvstore(self.source(), "")
        self.assertIn('const qualificationKVClockOffset = ""', neutral)

    def test_rejects_invalid_clock_offsets_and_unexpected_sources(self):
        with self.assertRaises(ValueError):
            host.rewrite_kvstore(self.source(), "+25 fortnights")
        with self.assertRaises(ValueError):
            host.rewrite_kvstore("package plugins\n", "")
        with self.assertRaises(ValueError):
            host.rewrite_kvstore(self.source() + host.KV_TTL, "")


class RewriteHTTPClientTest(unittest.TestCase):
    def source(self):
        return (
            "package plugins\n\nimport (\n"
            '\t"errors"\n'
            '\t"net/http"\n'
            ")\n\n"
            "func newHTTPService() {\n"
            "\tsvc.client = &http.Client{Transport: httpclient.NewTransport(svc.transport)}\n"
            "}\n"
        )

    def test_injects_env_gated_fixture_transport_without_changing_production_source(self):
        source = self.source()
        generated = host.rewrite_httpclient(source)
        self.assertIn('os.Getenv("BETTER_LYRICS_FIXTURES")', generated)
        self.assertIn("newQualificationFixtureTransport", generated)
        self.assertIn("fixture missing", generated)
        self.assertIn('"encoding/base64"', generated)
        self.assertIn('"encoding/json"', generated)
        self.assertIn('"os"', generated)
        self.assertEqual(source, self.source())

    def test_requires_exactly_one_production_hook(self):
        with self.assertRaises(ValueError):
            host.rewrite_httpclient("package plugins\n")
        with self.assertRaises(ValueError):
            host.rewrite_httpclient(self.source() + host.HOOK)


if __name__ == "__main__":
    unittest.main()
