import unittest

import browser_fixture_host as host


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
