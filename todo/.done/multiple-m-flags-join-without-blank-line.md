# Repeated -m flags join without a blank line between subject and body

git's own `commit -m a -m b` inserts a blank line between the two, making `a`
the subject and `b` the body. safegit's commit joins repeated `-m` values
without the blank line, so the whole message becomes one long subject and
`git log --oneline` prints everything.

Fix: join repeated `-m` values with `\n\n`, matching git. Red-green: two `-m`
flags produce a commit whose `%s` is the first value only and whose `%b` is
the second.

Small; observed live by a consumer session on 2026-08-13.
