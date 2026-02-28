#!/usr/bin/env python3
"""Minimal apply_patch for Codex-style patches from stdin."""
import sys, os

def main():
    patch = sys.stdin.read()
    lines = patch.strip().split('\n')
    i = 0
    while i < len(lines):
        line = lines[i]
        if line.startswith('*** Update File:'):
            path = line.split(':', 1)[1].strip()
            i += 1
            if i < len(lines) and lines[i].startswith('@@'):
                i += 1
            old, new = [], []
            while i < len(lines) and not lines[i].startswith('***'):
                if lines[i].startswith('-'):
                    old.append(lines[i][1:])
                elif lines[i].startswith('+'):
                    new.append(lines[i][1:])
                else:
                    old.append(lines[i][1:] if lines[i].startswith(' ') else lines[i])
                    new.append(lines[i][1:] if lines[i].startswith(' ') else lines[i])
                i += 1
            with open(path) as f:
                content = f.read()
            target = '\n'.join(old)
            if target not in content:
                print(f"ERROR: could not find target in {path}", file=sys.stderr)
                sys.exit(1)
            content = content.replace(target, '\n'.join(new), 1)
            with open(path, 'w') as f:
                f.write(content)
        elif line.startswith('*** Add File:'):
            path = line.split(':', 1)[1].strip()
            i += 1
            content_lines = []
            while i < len(lines) and not lines[i].startswith('***'):
                content_lines.append(lines[i][1:] if lines[i].startswith('+') else lines[i])
                i += 1
            os.makedirs(os.path.dirname(path) or '.', exist_ok=True)
            with open(path, 'w') as f:
                f.write('\n'.join(content_lines) + '\n')
        elif line.startswith('*** Delete File:'):
            path = line.split(':', 1)[1].strip()
            if os.path.exists(path):
                os.remove(path)
            i += 1
        else:
            i += 1

if __name__ == '__main__':
    main()
