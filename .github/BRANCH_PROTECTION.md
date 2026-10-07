# Branch Protection Configuration

ynr follows gitflow: `develop` is the default branch and takes feature PRs; `main` takes PRs only
from `develop`, `release/*` and `hotfix/*`. Each is protected by one repository ruleset. There is
no classic branch protection: the rulesets are the single source of truth. They are managed in
Terraform (`infra/github/branches.tf`), not by hand.

## Required checks

**All Clear** must pass before merging to either branch. It is the last job of the CI workflow,
depends on every other job (`check`, `s3`) and fails if any of them failed or was cancelled. Add or
rename CI jobs freely; keep All Clear's `needs` list in step.

Pull requests into `main` also require **Verify PR source branch** (`protect-main.yml`), which
fails for any source branch other than `develop`, `release/*` or `hotfix/*`.

## "Develop Branch Protection" (develop)

- Changes reach `develop` through a pull request (squash or merge commit; rebase is off)
- All review conversations must be resolved; no approving review is required
- All Clear must pass, and the branch must be up to date with `develop` before merging
- Force pushes blocked
- Branch deletion blocked
- Repository admins can bypass in emergencies

## "Main Branch Protection" (main)

Everything above for `main`, plus:

- Verify PR source branch must pass, so `main` moves only by release or hotfix
- Release and hotfix PRs are merge commits, so the back-merge into `develop` is clean
