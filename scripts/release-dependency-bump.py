"""Select a patch fallback only for an actual dependency-update commit title."""
import json
import re
import sys


def dependency_bump(event):
    message = (event.get("head_commit") or {}).get("message", "")
    lines = message.splitlines()
    title = lines[0] if lines else ""
    # WB lands a normal GitHub merge commit with the PR title after a blank line.
    if re.fullmatch(r"Merge pull request #[0-9]+ from \S+", title):
        title = lines[2] if len(lines) >= 3 and lines[1] == "" else ""
    return "patch" if re.match(r"^chore\(deps\): \S", title) else "false"


if __name__ == "__main__":
    print(dependency_bump(json.load(sys.stdin)))
