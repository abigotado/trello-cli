#!/usr/bin/env python3
"""Synchronize canonical .agents rules to tracked Cursor compatibility files."""

from __future__ import annotations

import argparse
import sys
from dataclasses import dataclass
from pathlib import Path


RULES = {
    "architecture": {
        "description": "trello-cli package boundaries and dependency direction.",
        "always_apply": True,
        "globs": None,
    },
    # The machine contract is the project's public API. Scoped to the packages
    # that define and render it, not to every .go file, so unrelated work does
    # not pull the whole contract into context.
    "cli-contract": {
        "description": "trello-cli JSON envelope, exit codes, and flag naming contract.",
        "always_apply": False,
        "globs": "internal/cli/**/*.go,internal/errx/**/*.go,internal/output/**/*.go",
    },
    "go-style": {
        "description": "trello-cli Go conventions: errors, context, resources, naming.",
        "always_apply": False,
        "globs": "**/*.go",
    },
    "testing": {
        "description": "trello-cli table-driven tests and boundary mocking conventions.",
        "always_apply": False,
        "globs": "**/*_test.go",
    },
    # Always applied: credential handling is a property of the whole binary,
    # and the failure mode is leaking a token, not a localized bug.
    "secrets": {
        "description": "trello-cli credential storage, redaction, and logging rules.",
        "always_apply": True,
        "globs": None,
    },
    # Always applied: the gate is a property of pushing any change, so there is
    # no file glob that usefully scopes it.
    "quality-gates": {
        "description": "trello-cli Git hook installation and the pre-push quality gate.",
        "always_apply": True,
        "globs": None,
    },
}


class RuleSyncError(RuntimeError):
    """Raised when repository layout cannot be synchronized safely."""


class RuleDriftError(RuleSyncError):
    """Raised when check mode finds missing, changed, or unexpected mirrors."""


@dataclass(frozen=True)
class SyncResult:
    updated: int
    removed: tuple[Path, ...]


def _render(source: Path, metadata: dict[str, object]) -> str:
    body = source.read_text(encoding="utf-8").rstrip() + "\n"
    lines = [
        "---",
        f"description: {metadata['description']}",
        f"alwaysApply: {str(metadata['always_apply']).lower()}",
    ]
    globs = metadata["globs"]
    if globs is not None:
        lines.append(f'globs: "{globs}"')
    lines.extend(["---", "", body])
    return "\n".join(lines)


def _assert_real_directory(path: Path, *, label: str) -> Path:
    if path.is_symlink() or not path.is_dir():
        raise RuleSyncError(f"{label} must be a real directory: {path}")
    return path.resolve()


def _assert_contained(path: Path, root: Path, *, label: str) -> Path:
    resolved = path.resolve()
    try:
        resolved.relative_to(root)
    except ValueError as exc:
        raise RuleSyncError(f"{label} escapes {root}: {path}") from exc
    return resolved


def _validate_tree(root: Path, *, label: str) -> tuple[Path, ...]:
    resolved_root = _assert_real_directory(root, label=label)
    files: list[Path] = []
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise RuleSyncError(f"{label} must not contain symlinks: {path}")
        _assert_contained(path, resolved_root, label=label)
        if path.suffix == ".mdc":
            if not path.is_file():
                raise RuleSyncError(f"{label} .mdc entry must be a file: {path}")
            files.append(path)
    return tuple(files)


def synchronize(repo: Path, *, check: bool) -> SyncResult:
    """Check or update the exact declared Cursor mirror inventory."""
    repo = repo.resolve()
    agents_root = repo / ".agents"
    cursor_parent = repo / ".cursor"
    _assert_real_directory(agents_root, label=".agents")
    _assert_real_directory(cursor_parent, label=".cursor")

    canonical_root = agents_root / "rules"
    cursor_root = cursor_parent / "rules"
    _assert_real_directory(canonical_root, label="canonical rules")
    existing_cursor_files = _validate_tree(cursor_root, label="Cursor rules")

    expected_canonical = {
        canonical_root / f"{name}.md" for name in RULES
    }
    actual_canonical = set(canonical_root.glob("*.md"))
    unexpected_canonical = sorted(actual_canonical - expected_canonical)
    missing_canonical = sorted(expected_canonical - actual_canonical)
    if unexpected_canonical or missing_canonical:
        details = [
            *(f"unexpected canonical rule: {path.relative_to(repo)}" for path in unexpected_canonical),
            *(f"missing canonical rule: {path.relative_to(repo)}" for path in missing_canonical),
        ]
        raise RuleSyncError("\n".join(details))

    expected: dict[Path, str] = {}
    for name, metadata in RULES.items():
        source = canonical_root / f"{name}.md"
        if source.is_symlink() or not source.is_file():
            raise RuleSyncError(f"missing real canonical rule: {source}")
        _assert_contained(source, canonical_root.resolve(), label="canonical rule")
        expected[cursor_root / f"{name}.mdc"] = _render(source, metadata)

    existing = set(existing_cursor_files)
    expected_paths = set(expected)
    extras = sorted(existing - expected_paths)
    drift = sorted(
        path
        for path, content in expected.items()
        if not path.is_file()
        or path.is_symlink()
        or path.read_text(encoding="utf-8") != content
    )

    if check and (extras or drift):
        details = [
            *(f"unexpected Cursor rule: {path.relative_to(repo)}" for path in extras),
            *(f"out of sync: {path.relative_to(repo)}" for path in drift),
        ]
        raise RuleDriftError("\n".join(details))

    if check:
        return SyncResult(updated=0, removed=())

    # The complete tree was validated before this point. Remove only stale,
    # contained, real .mdc files; never unlink a symlink or directory.
    for path in extras:
        if path.is_symlink() or not path.is_file():
            raise RuleSyncError(f"refusing to remove unsafe Cursor rule: {path}")
        _assert_contained(path, cursor_root.resolve(), label="stale Cursor rule")
    for path in extras:
        path.unlink()

    for path, content in expected.items():
        path.write_text(content, encoding="utf-8")
    return SyncResult(updated=len(expected), removed=tuple(extras))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check",
        action="store_true",
        help="report drift without writing compatibility mirrors",
    )
    args = parser.parse_args()

    repo = Path(__file__).resolve().parents[2]
    try:
        result = synchronize(repo, check=args.check)
    except RuleDriftError as error:
        print(error, file=sys.stderr)
        return 1
    except (OSError, UnicodeError, RuleSyncError) as error:
        print(error, file=sys.stderr)
        return 2

    if args.check:
        print(f"Cursor rule mirrors are in sync ({len(RULES)} files).")
    else:
        for path in result.removed:
            print(f"Removed stale Cursor rule: {path.relative_to(repo)}")
        print(f"Updated {result.updated} Cursor rule mirrors.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
