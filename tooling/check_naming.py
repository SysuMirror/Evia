"""Keep first-party event terminology consistent; dependency locks remain upstream data."""
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
forbidden = "tr" + "ack"
exempt = {"pnpm-lock.yaml", "backend/go.sum"}
paths = subprocess.check_output(
    ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=ROOT
).decode("utf-8").split("\0")
failures = []
for name in dict.fromkeys(filter(None, paths)):
    if name in exempt:
        continue
    path = ROOT / name
    if forbidden in name.lower():
        failures.append(name + ": forbidden path name")
    if not path.is_file():
        continue
    try:
        body = path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        continue
    for line, body_line in enumerate(body.splitlines(), start=1):
        if forbidden in body_line.lower():
            failures.append(f"{name}:{line}: forbidden terminology")
if failures:
    print("First-party terminology requires review:\n" + "\n".join(failures))
    sys.exit(1)
print("First-party terminology verified.")
