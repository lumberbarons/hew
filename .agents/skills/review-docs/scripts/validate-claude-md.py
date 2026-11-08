#!/usr/bin/env python3
"""Validate conventional and explicitly configured project instruction files.

Also validates @path imports (Claude Code memory syntax), .claude/rules/
directory, and detects leaked local preferences in shared files.
"""

from __future__ import annotations

import argparse
import fnmatch
import glob
import subprocess
import re
import json
from pathlib import Path

# Target for agent-instructions files: longer files consume more context
# and reduce adherence to the instructions that matter.
AGENT_MD_LINE_TARGET = 200

# The agent-instructions file names covered across harnesses.
AGENT_MD_NAMES = ('CLAUDE.md', 'AGENTS.md', 'AGENTS.override.md')


def find_git_root(path: Path) -> Path | None:
    """Find the git repository root for a given path."""
    try:
        result = subprocess.run(
            ['git', 'rev-parse', '--show-toplevel'],
            cwd=path if path.is_dir() else path.parent,
            capture_output=True,
            text=True
        )
        if result.returncode == 0:
            return Path(result.stdout.strip())
    except FileNotFoundError:
        pass
    return None


def is_gitignored(file_path: Path, git_root: Path | None) -> bool:
    """Check if a file is ignored by git."""
    if git_root is None:
        return False
    try:
        result = subprocess.run(
            ['git', 'check-ignore', '-q', str(file_path)],
            cwd=git_root,
            capture_output=True
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False


def extract_navigation_table_refs(content: str, directory: Path) -> tuple[list[str], list[str]]:
    """Extract file references from tables, separating nav refs from doc refs.

    Returns (nav_refs, doc_refs) where:
    - nav_refs: References to actual files/dirs that should exist in the directory
    - doc_refs: References in documentation tables describing subdirectory structure
    """
    nav_refs = []
    doc_refs = []
    lines = content.split('\n')
    in_table = False
    is_doc_table = False

    for i, line in enumerate(lines):
        # Detect start of table (separator row with dashes)
        if re.match(r'\|\s*-+\s*\|', line):
            in_table = True
            # Look back for section headers that indicate doc tables
            # Check for patterns like "### Per-Feature", "## File Layout", etc.
            is_doc_table = False
            for j in range(max(0, i - 5), i):
                header_line = lines[j].lower()
                if re.match(r'^#{1,6}\s+.*\b(per-|file layout|structure|template)\b', header_line):
                    is_doc_table = True
                    break
            continue

        # Detect end of table (blank line or non-table line)
        if in_table:
            if not line.strip() or not line.strip().startswith('|'):
                in_table = False
                is_doc_table = False
                continue

        # Extract refs from table rows
        if in_table:
            match = re.search(r'\|\s*`([^`]+)`\s*\|', line)
            if match:
                ref = match.group(1)
                if is_doc_table:
                    doc_refs.append(ref)
                else:
                    nav_refs.append(ref)

    return nav_refs, doc_refs


def extract_imports(content: str, file_path: Path) -> list[dict]:
    """Extract @path/to/file import references from content.

    Skips references inside fenced code blocks, inline code spans,
    email addresses, npm scopes (@org/pkg), and social handles (@username without /).
    """
    imports = []
    lines = content.split('\n')
    in_fence = False
    parent_dir = file_path.parent

    for line_num, line in enumerate(lines, 1):
        # Track fenced code blocks
        if re.match(r'^```', line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue

        # Remove inline code spans before scanning
        stripped = re.sub(r'`[^`]+`', '', line)

        # Find @path references
        for m in re.finditer(r'@([\w./_~-][\w./_~-]*)', stripped):
            ref = m.group(1).rstrip('.')

            # Skip email addresses: something@ref
            start = m.start()
            if start > 0 and stripped[start - 1] not in (' ', '\t', '\n', '(', '[', '"', "'", ',', ':'):
                continue

            # Explicit package/install wording distinguishes prose package
            # mentions without suppressing standalone extensionless imports.
            package_context = re.search(r'\b(?:install|add|package|dependency)\s+$',
                                        stripped[:start], re.IGNORECASE) or re.match(
                r'\s+(?:package|dependency)\b', stripped[m.end():], re.IGNORECASE)
            if (package_context and not ref.startswith(('.', '/', '~'))
                    and re.fullmatch(r'[\w.-]+/[\w.-]+', ref)):
                continue

            # Skip social handles (no slash → likely @username)
            if '/' not in ref and not ref.startswith(('.', '~')):
                continue

            # Resolve path
            resolved = Path(ref).expanduser()
            if not resolved.is_absolute():
                resolved = parent_dir / resolved
            resolved = resolved.resolve()

            imports.append({
                'path': ref,
                'line_number': line_num,
                'resolved_path': str(resolved),
                'exists': resolved.exists(),
            })

    return imports


def validate_imports(file_path: Path, content: str) -> list[dict]:
    """Validate @path import targets exist. Returns P2 issues for missing targets."""
    issues = []
    for imp in extract_imports(content, file_path):
        if not imp['exists']:
            issues.append({
                'type': 'broken_import',
                'severity': 'P2',
                'message': f"Import target does not exist: @{imp['path']} (line {imp['line_number']})",
            })
    return issues


def validate_rules_directory(rules_dir: Path, git_root: Path | None) -> list[dict]:
    """Validate .claude/rules/ directory structure and content."""
    issues = []
    if not rules_dir.is_dir():
        return issues

    for md_file in rules_dir.rglob('*.md'):
        if is_gitignored(md_file, git_root):
            continue

        content = md_file.read_text()
        rel = md_file.relative_to(rules_dir)

        # P3: empty rule files
        if not content.strip():
            issues.append({
                'type': 'empty_rule_file',
                'severity': 'P3',
                'message': f"Empty rule file: .claude/rules/{rel}",
            })
            continue

        # Parse YAML frontmatter for paths field
        fm_match = re.match(r'^---\s*\n(.*?)\n---\s*\n', content, re.DOTALL)
        if fm_match:
            fm_text = fm_match.group(1)
            paths_match = re.search(r'^paths:\s*\n((?:\s+-\s+.+\n?)+)', fm_text, re.MULTILINE)
            if not paths_match:
                # Try single-line: paths: ["glob"]
                paths_match = re.search(r'^paths:\s*\[(.+)\]', fm_text, re.MULTILINE)
                if paths_match:
                    globs = [g.strip().strip('"').strip("'") for g in paths_match.group(1).split(',')]
                else:
                    globs = []
            else:
                globs = re.findall(r'-\s+["\']?([^"\']+?)["\']?\s*$', paths_match.group(1), re.MULTILINE)

            for glob_pat in globs:
                # P2: invalid glob syntax
                try:
                    fnmatch.translate(glob_pat)
                except Exception:
                    issues.append({
                        'type': 'invalid_glob',
                        'severity': 'P2',
                        'message': f"Invalid glob syntax in .claude/rules/{rel}: {glob_pat}",
                    })
                    continue

                # P3: glob matches no files
                if git_root:
                    matches = list(git_root.glob(glob_pat))
                    if not matches:
                        issues.append({
                            'type': 'orphan_glob',
                            'severity': 'P3',
                            'message': f"Glob matches no files in .claude/rules/{rel}: {glob_pat}",
                        })

    return issues


def check_leaked_local_preferences(content: str, file_path: Path) -> list[dict]:
    """Detect personal/machine-specific values in shared agent-instructions files."""
    issues = []
    patterns = [
        (r'/Users/[a-zA-Z][\w.-]+/', 'macOS user home path'),
        (r'/home/[a-zA-Z][\w.-]+/', 'Linux user home path'),
        (r'C:\\Users\\[a-zA-Z][\w.-]+\\', 'Windows user home path'),
    ]
    localhost_pattern = r'localhost:\d+'

    lines = content.split('\n')
    in_fence = False

    for line_num, line in enumerate(lines, 1):
        if re.match(r'^```', line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue

        # Remove inline code spans
        stripped = re.sub(r'`[^`]+`', '', line)

        for pat, desc in patterns:
            if re.search(pat, stripped):
                issues.append({
                    'type': 'leaked_local_preference',
                    'severity': 'P3',
                    'message': f"Hardcoded {desc} in shared {file_path.name} (line {line_num}) — belongs in a personal, untracked instruction file",
                })

        if re.search(localhost_pattern, stripped):
            issues.append({
                'type': 'leaked_local_preference',
                'severity': 'P3',
                'message': f"localhost URL in shared {file_path.name} (line {line_num}) (may be developer-specific) — consider a personal, untracked file",
            })

    return issues


def validate_agent_md(file_path: Path, git_root: Path | None,
                     imports: bool | None = None) -> dict:
    """Validate a single agent-instructions file."""
    issues = []

    if not file_path.exists():
        return {"file": str(file_path), "exists": False, "issues": ["File not found"]}

    content = file_path.read_text()
    directory = file_path.parent

    # A tabular index is one valid form for a CLAUDE.md, not a required one.
    # A file of commands, conventions, and gotchas with no table is fine.
    table_pattern = r'\|.*\|.*\|.*\|'
    has_table = bool(re.search(table_pattern, content))

    # Size: agent-instructions files load into context every session, and
    # adherence drops as they grow.
    line_count = len(content.splitlines())
    if line_count > AGENT_MD_LINE_TARGET:
        issues.append({
            "type": "oversized",
            "severity": "P3",
            "message": (
                f"{line_count} lines, over the {AGENT_MD_LINE_TARGET}-line target — "
                "move task-specific procedures to a skill, path-specific conventions to "
                "scoped rule files (e.g. .claude/rules/), and reference material to a file read on demand"
            ),
        })

    # Where a table exists, a "When to read" column carries the routing information.
    # Its absence is polish, not a defect.
    if has_table and not re.search(r'\|\s*When to (read|use|run)\s*\|', content, re.IGNORECASE):
        issues.append({
            "type": "missing_column",
            "severity": "P4",
            "message": "Index table has no 'When to read/use/run' column",
        })

    # Extract referenced files/directories from tables
    nav_refs, doc_refs = extract_navigation_table_refs(content, directory)
    refs = nav_refs  # For backward compatibility in output

    # Only check existence for navigation refs (not doc refs describing subdirectory structure)
    for ref in nav_refs:
        ref_path = directory / ref.rstrip('/')
        if not ref_path.exists():
            issues.append({
                "type": "missing_reference",
                "severity": "P2",
                "message": f"Referenced path does not exist: {ref}"
            })

    # No check for "files present but not indexed". An index is a curated set of
    # pointers, not a directory listing; omission is usually the right editorial
    # choice, and flagging it drives CLAUDE.md toward the file-by-file descriptions
    # that waste context on every load.

    # Claude's file format defines @path imports. Other instruction files are
    # ordinary prose unless the project's explicit convention enables imports.
    if imports is None:
        imports = file_path.name == 'CLAUDE.md'
    if imports:
        issues.extend(validate_imports(file_path, content))

    # Check for leaked local preferences
    issues.extend(check_leaked_local_preferences(content, file_path))

    return {
        "file": str(file_path),
        "exists": True,
        "has_table": has_table,
        "references": refs,
        "issues": issues
    }


def is_in_build_directory(file_path: Path) -> bool:
    """Check if a file is inside a build/artifact or test-fixture directory.

    Fixture directories hold deliberately-broken documents used as test input.
    Validating them reports defects that are the whole point of the fixture.
    """
    skip_dirs = {
        'build', 'dist', 'out', 'target', '.next', '__pycache__', 'node_modules',
        'fixtures', '__fixtures__', 'testdata', '.git', '.hg', '.svn',
    }
    for parent in file_path.parents:
        if parent.name in skip_dirs:
            return True
    return False


def collect_instruction_files(path: Path, git_root: Path | None,
                              instructions: list[str], imports: list[str]) -> tuple[list[Path], set[Path]]:
    """Add configured paths/globs to conventional discovery, without duplicates.

    Patterns are relative to the repository root (or the supplied directory
    outside Git). Personal, ignored, fixture, and external files are excluded.
    A missing literal path remains in the result so it cannot look like a clean
    review. --imports also selects a file and explicitly opts it into imports.
    """
    root = git_root or (path if path.is_dir() else path.parent)
    candidates = [path] if path.is_file() else [
        p for name in AGENT_MD_NAMES for p in path.rglob(name)
    ]
    import_paths = set()
    for pattern in instructions + imports:
        full_pattern = str(root / pattern)
        matches = [Path(p) for p in glob.glob(full_pattern, recursive=True)]
        if not matches:
            matches = [Path(full_pattern)]
        for candidate in matches:
            try:
                candidate.resolve().relative_to(root.resolve())
            except ValueError:
                raise ValueError(f'Instruction path is outside the repository: {pattern}')
            candidates.append(candidate)
            if pattern in imports:
                import_paths.add(candidate.resolve())

    files = {}
    for candidate in candidates:
        if candidate.is_dir() or is_in_build_directory(candidate):
            continue
        try:
            candidate.resolve().relative_to(root.resolve())
        except ValueError:
            continue
        if candidate.name in ('CLAUDE.local.md', 'MEMORY.md'):
            continue
        if is_gitignored(candidate, git_root):
            continue
        files.setdefault(candidate.resolve(), candidate)
    return sorted(files.values()), import_paths


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', type=Path)
    parser.add_argument('--json', action='store_true')
    parser.add_argument('--instructions', action='append', default=[], metavar='PATH_OR_GLOB',
                        help='Additional shared instruction paths, relative to the repository root; repeatable')
    parser.add_argument('--imports', action='append', default=[], metavar='PATH_OR_GLOB',
                        help='Select instruction files with an explicit @path import convention; repeatable')
    args = parser.parse_args()
    path = args.path.absolute()
    if not path.exists():
        parser.error(f'Path not found: {args.path}')

    # Find git root once for the entire run
    git_root = find_git_root(path)

    try:
        files, import_paths = collect_instruction_files(path, git_root, args.instructions, args.imports)
    except ValueError as error:
        parser.error(str(error))
    results = [validate_agent_md(p, git_root,
                                imports=p.name == 'CLAUDE.md' or p.resolve() in import_paths)
               for p in files]

    # Validate .claude/rules/ directory if it exists
    rules_dir = (git_root or (path if path.is_dir() else path.parent)) / '.claude' / 'rules'
    rules_issues = validate_rules_directory(rules_dir, git_root)
    if rules_issues:
        results.append({
            "file": str(rules_dir),
            "exists": True,
            "has_table": False,
            "references": [],
            "issues": rules_issues,
        })

    if args.json:
        print(json.dumps(results, indent=2))
    else:
        for r in results:
            print(f"\n{r['file']}:")
            if not r['exists']:
                print("  NOT FOUND")
                continue
            if not r['issues']:
                print("  OK")
            else:
                for issue in r['issues']:
                    print(f"  [{issue['severity']}] {issue['type']}: {issue['message']}")


if __name__ == "__main__":
    main()
