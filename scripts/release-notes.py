#!/usr/bin/env python3
"""Print one dated version section from the changelog, or fail if absent."""
import argparse
import re
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('version')
parser.add_argument('--changelog', type=Path, default=Path('CHANGELOG.md'))
args = parser.parse_args()
source = args.changelog.read_text(encoding='utf-8')
heading = re.search(r'^## ' + re.escape(args.version) + r' - \d{4}-\d{2}-\d{2}$', source, re.M)
if heading is None:
    parser.exit(1, f'Missing dated changelog section for {args.version}\n')
notes = re.split(r'^## ', source[heading.end():], maxsplit=1, flags=re.M)[0]
print(notes.strip())
