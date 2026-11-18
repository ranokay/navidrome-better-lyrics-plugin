import contextlib
import io
import tempfile
import unittest
from pathlib import Path

import browser_fixture_host as host


def http_source():
    return (
        "package plugins\n\nimport (\n"
        '\t"errors"\n'
        '\t"net/http"\n'
        ")\n\n"
        "func newHTTPService() {\n"
        "\tsvc.client = &http.Client{Transport: httpclient.NewTransport(svc.transport)}\n"
        "}\n"
    )


def kv_source():
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


class RewriteKVStoreTest(unittest.TestCase):
    def test_overlays_ttl_clock(self):
        source = kv_source()
        generated = host.rewrite_kvstore(source, "+25 hours")
        self.assertIn("var notExpiredFilter", generated)
        self.assertIn("qualificationKVClockTTLModifier", generated)
        self.assertIn('const qualificationKVClockOffset = "+25 hours"', generated)
        self.assertIn("datetime('now', ?`", generated)
        self.assertNotEqual(source, generated)
        self.assertIn(host.KV_FILTER, source)
        self.assertNotIn("qualificationKVClockOffset", source)

    def test_rejects_missing_or_invalid_clock_offsets(self):
        for offset in (
            "",
            "25 hours",
            "+25 fortnights",
            "+25 hours\n",
            " +25 hours",
            "+31 days",
            "+3651 days",
            "-4000 days",
        ):
            with self.subTest(offset=offset):
                with self.assertRaises(ValueError):
                    host.rewrite_kvstore(kv_source(), offset)
        with self.assertRaises(ValueError):
            host.rewrite_kvstore("package plugins\n", "+25 hours")
        with self.assertRaises(ValueError):
            host.rewrite_kvstore(kv_source() + host.KV_TTL, "+25 hours")


class ParseArgsTest(unittest.TestCase):
    def parse(self, *extra):
        return host.parse_args(
            ["host", "package", "fixtures", "output", *extra]
        )

    def test_default_requests_no_clock_overlay(self):
        self.assertIsNone(self.parse().clock_offset)

    def test_accepts_a_valid_clock_offset(self):
        self.assertEqual(self.parse("--clock-offset", "+25 hours").clock_offset, "+25 hours")

    def test_rejects_an_offset_beyond_the_instance_salt_ttl(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as raised:
                self.parse("--clock-offset", "+3651 days")
        self.assertEqual(raised.exception.code, 2)

    def test_rejects_an_invalid_clock_offset_with_a_cli_error(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as raised:
                self.parse("--clock-offset", "+25 hours\n")
        self.assertEqual(raised.exception.code, 2)


class PrepareOverlaysTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.checkout = self.root / "host"
        (self.checkout / "plugins").mkdir(parents=True)
        (self.checkout / host.HTTPCLIENT).write_text(http_source())
        self.overlay_dir = self.root / "overlay"
        self.overlay_dir.mkdir()

    def test_default_build_is_http_only_without_a_kvstore(self):
        default_offset = host.parse_args(
            ["host", "package", "fixtures", "output"]
        ).clock_offset
        replacements, hashes = host.prepare_overlays(
            self.checkout, self.overlay_dir, default_offset
        )
        self.assertEqual(list(replacements), [str(self.checkout / host.HTTPCLIENT)])
        self.assertIn("overlaySHA256", hashes)
        self.assertNotIn("kvOverlaySHA256", hashes)
        self.assertFalse((self.overlay_dir / "host_kvstore.go").exists())

    def test_clock_offset_overlays_the_kvstore(self):
        (self.checkout / host.KVSTORE).write_text(kv_source())
        replacements, hashes = host.prepare_overlays(
            self.checkout, self.overlay_dir, "+25 hours"
        )
        self.assertEqual(len(replacements), 2)
        self.assertIn(str(self.checkout / host.KVSTORE), replacements)
        self.assertIn("kvOverlaySHA256", hashes)
        self.assertTrue((self.overlay_dir / "host_kvstore.go").is_file())

    def test_rejects_a_trailing_newline_before_overlaying(self):
        (self.checkout / host.KVSTORE).write_text(kv_source())
        with self.assertRaises(ValueError):
            host.prepare_overlays(self.checkout, self.overlay_dir, "+25 hours\n")


class RewriteHTTPClientTest(unittest.TestCase):
    def test_injects_env_gated_fixture_transport_without_changing_production_source(self):
        source = http_source()
        generated = host.rewrite_httpclient(source)
        self.assertIn('os.Getenv("BETTER_LYRICS_FIXTURES")', generated)
        self.assertIn("newQualificationFixtureTransport", generated)
        self.assertIn("fixture missing", generated)
        self.assertIn('"encoding/base64"', generated)
        self.assertIn('"encoding/json"', generated)
        self.assertIn('"os"', generated)
        self.assertIn(host.HOOK, source)
        self.assertNotIn("newQualificationFixtureTransport", source)

    def test_requires_exactly_one_production_hook(self):
        with self.assertRaises(ValueError):
            host.rewrite_httpclient("package plugins\n")
        with self.assertRaises(ValueError):
            host.rewrite_httpclient(http_source() + host.HOOK)


if __name__ == "__main__":
    unittest.main()
