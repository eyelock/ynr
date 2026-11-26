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

## How a pull request is merged

A pull request into `develop` is squashed; one into `main` (a release or hotfix) is merged with a
merge commit, so `main`'s history is kept. A push to `main` or `develop` is never cancelled by the
next one. The one exception is the back-merge: once per release, `main` is merged back into
`develop` with a pull request from `main` into `develop`, and that must land as a merge commit, since
a squash would drop `main`'s history out of `develop`. A repository admin merges it, choosing
**Create a merge commit**, with the ruleset bypass (**Merge without waiting for requirements**) if
a required check is outstanding.

## "Develop Branch Protection" (develop)

- Changes reach `develop` through a pull request (squashed when merged; rebase is off)
- All review conversations must be resolved; no approving review is required
- All Clear must pass; the branch need not be up to date with `develop`
- Force pushes blocked
- Branch deletion blocked
- Repository admins can bypass in emergencies

## "Main Branch Protection" (main)

Everything above for `main`, plus:

- Verify PR source branch must pass, so `main` moves only by release or hotfix
- Release and hotfix PRs land as merge commits, so `main`'s history is kept

## "Never Delete Main or Develop" (main and develop)

- Branch deletion blocked and force pushes blocked, on both branches
- No bypass actors: nobody can bypass it, repository admins included, unlike the two rulesets above
- To delete or rewrite either branch on purpose, disable this ruleset first (change `enforcement`
  in `infra/github/branches.tf` and apply), then restore it
