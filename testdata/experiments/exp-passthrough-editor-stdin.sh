#!/usr/bin/env bash
# What does a passthrough's git child -- and the editor git launches from it --
# actually get on stdin?
#
# The question is not academic. `safegit rebase` reaches git through the effects
# handle (runGitMutation), and the effects handle assigns its children no stdin
# at all. `safegit cherry-pick` reaches git through runPassthrough, which
# inherits the parent's. So the two families differ, and the operation lock's
# documentation ("held for the whole passthrough, including rebase -i's editor")
# is only meaningful if that editor can in fact run.
#
# The probe runs `safegit rebase -i` and `safegit cherry-pick` under a
# pseudo-terminal, with an editor script that records: what fd 0 is, whether
# reading a line from stdin works, and whether /dev/tty can be opened and read.
#
# Usage: testdata/experiments/exp-passthrough-editor-stdin.sh
#
# Recorded result (git 2.51, Linux, safegit at the time the operation lock was
# added):
#
#   safegit rebase -i   editor RAN and the rebase completed. fd 0 was /dev/null,
#   (effects handle)    so reading stdin failed, but /dev/tty opened AND read.
#   git rebase -i       editor ran with fd 0 on the pty; stdin readable.
#   (control)
#   safegit cherry-pick editor ran with fd 0 on the pty; stdin readable.
#   (runPassthrough)
#
# So: an interactive passthrough DOES reach its editor under safegit, and a
# terminal editor (vim, nano, emacs -nw -- all of which open /dev/tty) works
# through either family. What the effects-handle family cannot do is feed an
# editor, or any other git child, from stdin: it sees EOF immediately. The
# operation lock is held across all of it.
set -euo pipefail

SAFEGIT="$(cd "$(dirname "$0")/../.." && pwd)/safegit"
DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT

# The probe's own files live OUTSIDE the repository. Left inside, they are
# untracked files and safegit's coordination guard refuses every passthrough
# before git is ever reached -- which would make the probe measure the guard
# instead of the editor.
REPO="$DIR/repo"
mkdir -p "$REPO"
export REPORT="$DIR/report.txt"

cat > "$DIR/editor.sh" <<'EDITOR'
#!/usr/bin/env bash
# $1 is the file git wants edited. Report the environment, then leave the file
# alone so the operation completes.
{
  echo "  editor ran: yes"
  echo "  fd 0 -> $(readlink /proc/self/fd/0 2>/dev/null || echo '(unreadable)')"
  if read -r -t 2 line < /dev/stdin 2>/dev/null; then
    echo "  read from stdin: yes (${line:0:20})"
  else
    echo "  read from stdin: no (rc=$?)"
  fi
  if exec 9< /dev/tty 2>/dev/null; then
    echo "  open /dev/tty: yes"
    if read -r -t 2 line <&9 2>/dev/null; then
      echo "  read from /dev/tty: yes (${line:0:20})"
    else
      echo "  read from /dev/tty: no (rc=$?)"
    fi
    exec 9<&-
  else
    echo "  open /dev/tty: no"
  fi
} >> "$REPORT"
EDITOR
chmod +x "$DIR/editor.sh"

cd "$REPO"
git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
echo one > f.txt && git add f.txt && git commit -q -m one
echo two >> f.txt && git commit -q -am two
echo three >> f.txt && git commit -q -am three

export GIT_SEQUENCE_EDITOR="$DIR/editor.sh"
export GIT_EDITOR="$DIR/editor.sh"

run_under_pty() {
	# A pseudo-terminal is what makes the probe meaningful: it gives the child a
	# controlling terminal, so "could not read stdin" can be told apart from
	# "there was no terminal anywhere". python's pty.spawn is used rather than
	# script(1), which is not present on every machine this runs on.
	python3 - "$@" <<'PY' > "$DIR/pty.out" 2>&1 || true
import os, pty, sys

typed = b"typed-into-the-terminal\n"
sent = False

def stdin_read(fd):
	# Feed one line into the terminal once, then EOF-ish silence.
	global sent
	if sent:
		return b""
	sent = True
	return typed

pty.spawn(sys.argv[1:], master_read=lambda fd: os.read(fd, 1024), stdin_read=stdin_read)
PY
	sed 's/^/  | /' "$DIR/pty.out" >> "$REPORT"
}

echo "== safegit rebase -i (runGitMutation: through the effects handle) ==" >> "$REPORT"
run_under_pty "$SAFEGIT" rebase -i HEAD~2
if ! grep -q "editor ran" "$REPORT"; then echo "  editor ran: NO" >> "$REPORT"; fi
git rebase --abort 2>/dev/null || true

echo "== git rebase -i directly (control) ==" >> "$REPORT"
before=$(grep -c "editor ran" "$REPORT" || true)
run_under_pty git rebase -i HEAD~2
after=$(grep -c "editor ran" "$REPORT" || true)
if [ "$before" = "$after" ]; then echo "  editor ran: NO" >> "$REPORT"; fi
git rebase --abort 2>/dev/null || true

echo "== safegit cherry-pick --edit (runPassthrough: inherits stdin) ==" >> "$REPORT"
git checkout -q -b side HEAD~2
echo side > g.txt && git add g.txt && git commit -q -m side
git checkout -q main
before=$(grep -c "editor ran" "$REPORT" || true)
run_under_pty "$SAFEGIT" cherry-pick --edit side
after=$(grep -c "editor ran" "$REPORT" || true)
if [ "$before" = "$after" ]; then echo "  editor ran: NO" >> "$REPORT"; fi

cat "$REPORT"
