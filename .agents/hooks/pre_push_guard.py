#!/usr/bin/env python3

import json
import os
import shlex
import subprocess
import sys
from pathlib import Path
from typing import List, NamedTuple, Optional


class GitPushInvocation(NamedTuple):
    arguments: List[str]
    overrides_hooks_path: bool


_OPTIONS_WITH_VALUES = {
    "-C",
    "-c",
    "--config-env",
    "--exec-path",
    "--git-dir",
    "--namespace",
    "--work-tree",
}

_SHELL_NAMES = {"bash", "dash", "ksh", "sh", "zsh"}
_SHELL_OPTIONS_WITH_VALUES = {
    "+O",
    "+o",
    "-O",
    "-o",
    "--init-file",
    "--rcfile",
}
_NO_VERIFY_OPTION = "--no-verify"
_TRACKED_HOOKS_PATH = ".githooks"
_GATE_REFERENCE = ".githooks/pre-push"


def _is_hooks_path_config(value: str) -> bool:
    return value.split("=", 1)[0].lower() == "core.hookspath"


def _has_git_config_environment_override(tokens: List[str]) -> bool:
    for token in tokens:
        name, separator, _ = token.partition("=")
        if separator and name.upper().startswith("GIT_CONFIG_"):
            return True
    return False


def _has_git_config_hooks_path_command(tokens: List[str]) -> bool:
    for index, token in enumerate(tokens):
        if Path(token).name != "git":
            continue

        cursor = index + 1
        while cursor < len(tokens) and tokens[cursor].startswith("-"):
            option = tokens[cursor]
            cursor += 1
            option_name, separator, inline_value = option.partition("=")
            if option.startswith("-c") and option != "-c":
                option_name = "-c"
                inline_value = option[2:]
                separator = "="
            if option_name in _OPTIONS_WITH_VALUES and not separator:
                cursor += 1

        if cursor >= len(tokens) or tokens[cursor] != "config":
            continue

        if any(
            _is_hooks_path_config(argument.strip(";&|()"))
            for argument in tokens[cursor + 1 :]
        ):
            return True

    return False


def _shell_commands(tokens: List[str]) -> List[str]:
    commands: List[str] = []
    for index, token in enumerate(tokens):
        if Path(token).name not in _SHELL_NAMES:
            continue

        cursor = index + 1
        while cursor < len(tokens):
            option = tokens[cursor]
            if option == "--":
                break

            if option in _SHELL_OPTIONS_WITH_VALUES:
                cursor += 2
                continue

            if option.startswith(("--init-file=", "--rcfile=")):
                cursor += 1
                continue

            if (
                not option.startswith(("-", "+"))
                or option in {"-", "+"}
            ):
                break

            cursor += 1
            if option.startswith("--"):
                continue
            if "c" in option[1:] and cursor < len(tokens):
                commands.append(tokens[cursor])
                break
    return commands


def _direct_git_push_invocation(tokens: List[str]) -> Optional[GitPushInvocation]:
    try:
        overrides_hooks_path_before_push = (
            _has_git_config_environment_override(tokens)
            or _has_git_config_hooks_path_command(tokens)
        )
    except (AttributeError, TypeError):
        return None

    for index, token in enumerate(tokens):
        if Path(token).name != "git":
            continue

        cursor = index + 1
        overrides_hooks_path = False
        while cursor < len(tokens) and tokens[cursor].startswith("-"):
            option = tokens[cursor]
            cursor += 1

            if option == "--":
                break

            option_name, separator, inline_value = option.partition("=")
            if option.startswith("-c") and option != "-c":
                option_name = "-c"
                inline_value = option[2:]
                separator = "="

            if option_name not in _OPTIONS_WITH_VALUES:
                continue

            value = inline_value if separator else ""
            if not value and cursor < len(tokens):
                value = tokens[cursor]
                cursor += 1

            if option_name in {"-c", "--config-env"} and _is_hooks_path_config(
                value
            ):
                overrides_hooks_path = True

        if cursor < len(tokens) and tokens[cursor] == "push":
            return GitPushInvocation(
                tokens[cursor + 1 :],
                overrides_hooks_path or overrides_hooks_path_before_push,
            )

    return None


def _git_push_invocation(command: str) -> Optional[GitPushInvocation]:
    try:
        tokens = shlex.split(command)
    except (TypeError, ValueError):
        return None

    invocation = _direct_git_push_invocation(tokens)
    if invocation is not None:
        return invocation

    for nested_command in _shell_commands(tokens):
        invocation = _git_push_invocation(nested_command)
        if invocation is not None:
            if _has_git_config_environment_override(tokens):
                return GitPushInvocation(invocation.arguments, True)
            return invocation

    return None


def _is_no_verify_option(argument: str) -> bool:
    option = argument.split("=", 1)[0]
    return (
        option.startswith("--no-v")
        and _NO_VERIFY_OPTION.startswith(option)
    )


def _git_output(working_directory: str, arguments: List[str]) -> Optional[str]:
    result = subprocess.run(
        ["git", "-C", working_directory, *arguments],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def _effective_hooks_directory(
    working_directory: str,
    repository_root: Path,
    configured: Optional[str],
) -> Optional[Path]:
    """Resolve the directory Git will actually take this push's hooks from.

    Read across every config scope, not just `--local`: a global or system
    `core.hooksPath` governs the push just as much as a repository-local one.
    """
    if configured:
        path = Path(configured)
        # Git interprets a relative core.hooksPath from the top level of the
        # working tree, which is what makes the tracked `.githooks` value work.
        return path if path.is_absolute() else repository_root / path

    common_directory = _git_output(working_directory, ["rev-parse", "--git-common-dir"])
    if common_directory is None:
        return None
    path = Path(common_directory)
    if not path.is_absolute():
        path = Path(working_directory) / path
    return path / "hooks"


def _runs_quality_gate(hook: Path, repository_root: Path) -> bool:
    """Report whether this pre-push hook actually runs the trello-cli gate.

    The question is whether the gate executes, not whether `core.hooksPath`
    holds a particular string. A developer who keeps their own hooks directory
    and chains `.githooks/pre-push` from it runs the gate in full, and must not
    be forced to repoint `core.hooksPath` — that would silently stop every
    other hook in their directory, since Git does not fall back to it.
    """
    if not hook.is_file() or not os.access(hook, os.X_OK):
        return False

    gate = repository_root / ".githooks" / "pre-push"
    try:
        if hook.resolve() == gate.resolve():
            return True
    except OSError:
        return False

    if not gate.is_file():
        return False
    try:
        return _GATE_REFERENCE in hook.read_text(errors="ignore")
    except OSError:
        return False


def _not_installed_reason(configured: Optional[str]) -> str:
    if configured and configured != _TRACKED_HOOKS_PATH:
        return (
            f"trello-cli pre-push gate does not run: core.hooksPath is "
            f"'{configured}' and its pre-push does not chain the gate. "
            f"Keep your hooks directory and add "
            f"'exec {_GATE_REFERENCE} \"$@\"' to its pre-push, or run "
            f".githooks/install to hand every hook to .githooks."
        )
    return (
        "trello-cli pre-push hook is not installed. "
        "Run .githooks/install, then retry the push."
    )


def _deny(reason: str) -> None:
    print(
        json.dumps(
            {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": reason,
                }
            }
        )
    )


def main() -> int:
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, TypeError):
        _deny("trello-cli push guard received invalid hook input; command denied.")
        return 0

    if not isinstance(payload, dict):
        _deny("trello-cli push guard received invalid hook input; command denied.")
        return 0

    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        _deny("trello-cli push guard received invalid hook input; command denied.")
        return 0

    command = tool_input.get("command")
    if not isinstance(command, str):
        _deny("trello-cli push guard received invalid hook input; command denied.")
        return 0

    invocation = _git_push_invocation(command)
    if invocation is None:
        return 0

    if invocation.overrides_hooks_path:
        _deny(
            "trello-cli does not allow overriding core.hooksPath for git push. "
            "Use the repository-managed .githooks path."
        )
        return 0

    if any(_is_no_verify_option(argument) for argument in invocation.arguments):
        _deny(
            "trello-cli does not allow git push --no-verify. "
            "Fix the pre-push failure and push normally."
        )
        return 0

    working_directory = payload.get("cwd") or os.getcwd()
    repository_root = _git_output(working_directory, ["rev-parse", "--show-toplevel"])
    if repository_root is None:
        _deny("trello-cli push guard cannot resolve the repository; command denied.")
        return 0

    root = Path(repository_root)
    configured = _git_output(working_directory, ["config", "--get", "core.hooksPath"])
    hooks_directory = _effective_hooks_directory(working_directory, root, configured)
    if hooks_directory is None or not _runs_quality_gate(
        hooks_directory / "pre-push", root
    ):
        _deny(_not_installed_reason(configured))

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
