# CI macOS cost reduction

## Problem

safegit's CI costs $8.99/month -- the most expensive of all 60 repos -- because the CI matrix runs on both `ubuntu-latest` and `macos-latest` with 2 Go versions. macOS runners cost 10x the Linux rate ($0.062/min vs $0.006/min). With 48 releases in the last 6 weeks and a 49% CI failure rate, each failed run still burns macOS minutes.

## Numbers

- 72 CI runs, 35 failures (49%), 11 manual retriggers (all failed)
- Each CI run: 4 jobs (2 OS x 2 Go versions)
- macOS jobs: ~2 min each, billed at 10x = 40 weighted minutes per CI run
- Ubuntu jobs: ~1 min each, billed at 1x = 2 weighted minutes per CI run
- macOS accounts for 92% of safegit's CI cost

## Fix

Drop `macos-latest` from the CI matrix in `.github/workflows/ci.yml`. safegit is a pure Go CLI wrapping git -- platform-specific bugs on macOS are unlikely. Options:

1. **Remove macOS entirely.** Simplest. Ubuntu-only CI. Cuts cost by ~90%.
2. **macOS on releases only.** Add a separate workflow or condition that runs macOS tests only on release tags. Catches platform issues before publish without per-push cost.
3. **Weekly macOS.** Scheduled cron job runs macOS tests once a week. Catches regressions without per-push cost.

Also investigate the 49% CI failure rate -- that's a lot of wasted minutes regardless of runner type.

## Impact

Estimated savings: ~$8/month (from ~$9 to ~$1). Across a year: ~$96.
