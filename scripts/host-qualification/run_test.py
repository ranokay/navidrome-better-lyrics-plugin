"""Regressions for local qualification input validation and temporary state."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("qualification_runner", Path(__file__).with_name("run.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.checkout = self.root / "trusted host"
        self.checkout.mkdir()
        (self.checkout / "plugins").mkdir()
        (self.checkout / "go.mod").write_text("module github.com/navidrome/navidrome\n")
        self.package = self.root / "trusted plugin.ndp"
        self.package.write_bytes(b"test package")

    def invoke(self, checkout=None, package=None):
        argv = ["run.py", str(checkout or self.checkout), str(package or self.package)]
        with patch.object(sys, "argv", argv), contextlib.redirect_stdout(io.StringIO()):
            return runner.main()

    def test_invalid_paths_fail_before_executing_git_or_go(self):
        cases = [(self.root / "missing", self.package),
                 (self.package, self.package), (self.checkout, self.checkout)]
        for checkout, package in cases:
            with self.subTest(checkout=checkout, package=package):
                with patch.object(runner.subprocess, "check_output") as git:
                    with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                        self.invoke(checkout, package)
                    self.assertEqual(error.exception.code, 2)
                    git.assert_not_called()

    def test_wrong_module_is_rejected_before_execution(self):
        (self.checkout / "go.mod").write_text("module example.org/other\n")
        with patch.object(runner.subprocess, "check_output") as git:
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.invoke()
            git.assert_not_called()

    def test_checkout_must_be_worktree_root(self):
        with patch.object(runner, "git_output", return_value=str(self.root)):
            with patch.object(runner.subprocess, "run") as go:
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    self.invoke()
                go.assert_not_called()

    def test_git_ignores_inherited_repository_overrides_and_external_diff(self):
        with patch.dict(os.environ, {"GIT_DIR": "/other", "GIT_CONFIG_COUNT": "1"}):
            with patch.object(runner.subprocess, "check_output", return_value="") as git:
                runner.host_state(self.checkout)
        for call in git.call_args_list:
            self.assertFalse(call.kwargs["shell"])
            self.assertNotIn("GIT_DIR", call.kwargs["env"])
            self.assertNotIn("GIT_CONFIG_COUNT", call.kwargs["env"])
            self.assertIn("core.fsmonitor=false", call.args[0])
        self.assertIn("--no-ext-diff", git.call_args_list[-1].args[0])
        self.assertIn("--no-textconv", git.call_args_list[-1].args[0])

    def test_failed_go_test_cleans_snapshot_and_checks_host_state(self):
        snapshots = []

        def failed_test(command, **kwargs):
            self.assertEqual(command[:2], ["go", "test"])
            self.assertEqual(kwargs["cwd"], self.checkout)
            self.assertFalse(kwargs["shell"])
            snapshot = Path(kwargs["env"]["BETTER_LYRICS_PACKAGE"])
            self.assertEqual(snapshot.read_bytes(), self.package.read_bytes())
            snapshots.append(snapshot)
            return type("Result", (), {"returncode": 1})()

        # A trusted checkout outside the plugin workspace is an intended use.
        with patch.object(runner, "git_output", return_value=str(self.checkout)):
            with patch.object(runner, "host_state", return_value=("dirty", "diff")) as state:
                with patch.object(runner.shutil, "which", return_value="/trusted/go"):
                    with patch.object(runner.subprocess, "run", side_effect=failed_test):
                        self.assertEqual(self.invoke(), 1)
                self.assertEqual(state.call_count, 2)
        self.assertTrue(snapshots)
        self.assertFalse(snapshots[0].parent.exists())


if __name__ == "__main__":
    unittest.main()
