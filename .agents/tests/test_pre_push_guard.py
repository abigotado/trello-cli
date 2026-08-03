from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "hooks" / "pre_push_guard.py"
SPEC = importlib.util.spec_from_file_location("trello_cli_pre_push_guard", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
GUARD = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = GUARD
SPEC.loader.exec_module(GUARD)


GATE_BODY = "#!/usr/bin/env bash\necho 'pre-push: ok'\n"


def _environment_without_git() -> dict[str, str]:
    """Every GIT_* variable dropped.

    Git exports GIT_DIR, GIT_INDEX_FILE and friends to hook processes, and
    they outrank `-C`. This is not a hypothetical: the first version of these
    tests inherited GIT_DIR from the pre-push gate, and `git config` in the
    fixture setup wrote into the real repository instead of the temporary one.
    """
    return {
        name: value
        for name, value in os.environ.items()
        if not name.startswith("GIT_")
    }


def _git(repository: Path, *arguments: str) -> None:
    # env is passed explicitly rather than relying on the caller's scrubbing,
    # so a fixture can never reach a repository outside its temporary tree.
    subprocess.run(
        ["git", "-C", str(repository), *arguments],
        check=True,
        capture_output=True,
        text=True,
        env=_environment_without_git(),
    )


class _HermeticGitTest(unittest.TestCase):
    """Base class that removes inherited GIT_* variables for each test.

    `_git` scrubs its own environment, but the guard shells out to `git` on
    its own account, so the ambient environment has to be clean too.
    """

    def setUp(self) -> None:
        patcher = mock.patch.dict(
            os.environ, _environment_without_git(), clear=True
        )
        patcher.start()
        self.addCleanup(patcher.stop)


class QualityGateDetectionTest(_HermeticGitTest):
    """Cover the paths that decide whether the tracked gate will run.

    These build real repositories and a real linked worktree rather than
    stubbing `git`: the bug this guards against was entirely about how Git
    reports paths from a worktree, which a stub would have reproduced wrongly.
    """

    def _repository(self, root: Path) -> Path:
        repository = root / "main"
        (repository / ".githooks").mkdir(parents=True)
        gate = repository / ".githooks" / "pre-push"
        gate.write_text(GATE_BODY, encoding="utf-8")
        gate.chmod(0o755)
        _git(repository.parent, "init", "-q", "main")
        _git(repository, "config", "user.email", "test@example.com")
        _git(repository, "config", "user.name", "Test")
        _git(repository, "add", ".")
        _git(repository, "commit", "-qm", "initial")
        return repository

    def test_gate_in_the_checkout_itself_is_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repository = self._repository(Path(temporary))

            self.assertTrue(
                GUARD._runs_quality_gate(
                    repository / ".githooks" / "pre-push",
                    repository,
                    str(repository),
                )
            )

    def test_main_worktree_gate_is_accepted_from_a_linked_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repository = self._repository(Path(temporary))
            worktree = Path(temporary) / "linked"
            _git(repository, "worktree", "add", "-q", str(worktree), "-b", "topic")

            # What `.githooks/install` leaves behind: an absolute path into the
            # main checkout, inherited by every worktree created afterwards.
            hook = repository / ".githooks" / "pre-push"

            self.assertNotEqual(hook.resolve(), (worktree / ".githooks" / "pre-push").resolve())
            self.assertTrue(
                GUARD._runs_quality_gate(hook, worktree, str(worktree)),
                "a worktree pointed at the main checkout's gate must count as installed",
            )

    def test_unrelated_hook_that_does_not_chain_the_gate_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repository = self._repository(Path(temporary))
            other = repository / "elsewhere"
            other.mkdir()
            hook = other / "pre-push"
            hook.write_text("#!/usr/bin/env bash\necho unrelated\n", encoding="utf-8")
            hook.chmod(0o755)

            self.assertFalse(
                GUARD._runs_quality_gate(hook, repository, str(repository))
            )

    def test_custom_hook_chaining_the_gate_is_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repository = self._repository(Path(temporary))
            other = repository / "elsewhere"
            other.mkdir()
            hook = other / "pre-push"
            hook.write_text(
                '#!/usr/bin/env bash\nexec .githooks/pre-push "$@"\n',
                encoding="utf-8",
            )
            hook.chmod(0o755)

            self.assertTrue(
                GUARD._runs_quality_gate(hook, repository, str(repository))
            )

    def test_non_executable_gate_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repository = self._repository(Path(temporary))
            hook = repository / ".githooks" / "pre-push"
            hook.chmod(0o644)

            self.assertFalse(
                GUARD._runs_quality_gate(hook, repository, str(repository))
            )


class MainWorktreeRootTest(_HermeticGitTest):
    def test_root_is_found_from_the_checkout_a_subdirectory_and_a_worktree(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repository = root / "main"
            (repository / "nested").mkdir(parents=True)
            (repository / "nested" / "file.txt").write_text("x\n", encoding="utf-8")
            _git(root, "init", "-q", "main")
            _git(repository, "config", "user.email", "test@example.com")
            _git(repository, "config", "user.name", "Test")
            _git(repository, "add", ".")
            _git(repository, "commit", "-qm", "initial")
            worktree = root / "linked"
            _git(repository, "worktree", "add", "-q", str(worktree), "-b", "topic")

            expected = repository.resolve()
            for label, working_directory in (
                ("checkout root", repository),
                ("subdirectory", repository / "nested"),
                ("linked worktree", worktree),
            ):
                with self.subTest(label):
                    found = GUARD._main_worktree_root(str(working_directory))

                    self.assertIsNotNone(found)
                    assert found is not None
                    self.assertEqual(found.resolve(), expected)

    def test_outside_a_repository_returns_none(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            self.assertIsNone(GUARD._main_worktree_root(temporary))


class PushInvocationTest(unittest.TestCase):
    """The parsing that decides whether the guard engages at all."""

    def test_no_verify_and_hooks_path_overrides_are_still_refused(self) -> None:
        cases = (
            ("git push --no-verify", "--no-verify"),
            ("git push --no-v", "--no-verify"),
            ("git -c core.hooksPath=/dev/null push", "core.hooksPath"),
            ("bash -c 'git push --no-verify'", "--no-verify"),
        )
        for command, expected in cases:
            with self.subTest(command):
                invocation = GUARD._git_push_invocation(command)

                self.assertIsNotNone(invocation)
                assert invocation is not None
                if expected == "core.hooksPath":
                    self.assertTrue(invocation.overrides_hooks_path)
                else:
                    self.assertTrue(
                        any(
                            GUARD._is_no_verify_option(argument)
                            for argument in invocation.arguments
                        )
                    )

    def test_a_command_that_is_not_a_push_is_ignored(self) -> None:
        for command in ("git status", "go test ./...", "git fetch origin"):
            with self.subTest(command):
                self.assertIsNone(GUARD._git_push_invocation(command))

    def test_a_git_push_mentioned_as_an_argument_still_engages_the_guard(self) -> None:
        """Deliberate over-match: the token scan cannot tell quoting from intent.

        Engaging on `echo git push` costs a denial the developer can reword.
        Missing a real push costs the gate, so this fails closed on purpose.
        """
        self.assertIsNotNone(GUARD._git_push_invocation("echo git push"))


# Deliberately no test asserting the guard accepts *this* checkout. Whether
# core.hooksPath is set here depends on the developer having run
# .githooks/install, so such a test reports the environment rather than the
# code, and fails for a contributor who has not installed the hooks. The
# linked-worktree fixture above covers the same path hermetically.


if __name__ == "__main__":
    unittest.main()
