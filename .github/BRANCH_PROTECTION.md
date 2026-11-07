# Branch Protection Configuration

ynr follows gitflow: `develop` is the default branch and takes feature PRs; `main` takes PRs only
from `develop`, `release/*` and `hotfix/*`. Each is protected by one repository ruleset, and a third ruleset covers both against deletion and force pushes. There is
no classic branch protection: the rulesets are the single source of truth. They are managed in
Terraform (`infra/github/branches.tf`), not by hand.

## Required checks

**All Clear** must pass before merging to either branch. It is the last job of the CI workflow,
depends on every other job (`check`, `s3`) and fails if any of them failed or was cancelled. Add or
rename CI jobs freely; keep All Clear's `needs` list in step.

Pull requests into `main` also require **Verify PR source branch** (`protect-main.yml`), which
fails for any source branch other than `develop`, `release/*` or `hotfix/*`.

## Merge queue

Both branches use the merge queue: a pull request is added to the queue instead of merged, and is
tested together with those ahead of it on a `gh-readonly-queue/*` branch before it lands. Two pull
requests that are each green but break each other can no longer both reach the branch. The required
checks therefore run on the `merge_group` event too (`ci.yml`, `protect-main.yml`); a workflow that
a ruleset requires must keep that trigger, or the queue waits forever for a check that never
reports. A push to `main` or `develop` is never cancelled by the next one. The queue merges by
squash on `develop` and by merge commit on `main`.

## "Develop Branch Protection" (develop)

- Changes reach `develop` through a pull request (squash or merge commit; rebase is off)
- All review conversations must be resolved; no approving review is required
- All Clear must pass; the branch need not be up to date with `develop`
- Force pushes blocked
- Branch deletion blocked
- Repository admins can bypass in emergencies

## "Main Branch Protection" (main)

Everything above for `main`, plus:

- Verify PR source branch must pass, so `main` moves only by release or hotfix
- Release and hotfix PRs are merge commits, so the back-merge into `develop` is clean

## "Never Delete Main or Develop" (main and develop)

- Branch deletion blocked and force pushes blocked, on both branches
- No bypass actors: nobody can bypass it, repository admins included, unlike the two rulesets above
- To delete or rewrite either branch on purpose, disable this ruleset first (change `enforcement`
  in `infra/github/branches.tf` and apply), then restore it
