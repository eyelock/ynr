# GitHub repository

Terraform for the `eyelock/ynr` repository's settings, so the repository can be checked for drift
and set up again from nothing.

| File | What it manages |
|---|---|
| `repository.tf` | The repository: description, topics, visibility, features, merge options |
| `branches.tf` | Gitflow: `develop` as the default branch, and a repository ruleset on each of `develop` and `main`: a pull request with conversations resolved and "All Clear" green (plus "Verify PR source branch" into `main`, so it takes only `develop`, `release/*` and `hotfix/*`), no force-push or delete, repository admins can bypass. See [`.github/BRANCH_PROTECTION.md`](../../.github/BRANCH_PROTECTION.md) |
| `labels.tf` | Issue and PR labels, authoritatively: a label not listed is removed |
| `actions.tf` | Actions permissions, the read-only default `GITHUB_TOKEN`, and that the `RELEASE_TOKEN` secret exists |
| `security.tf` | Dependabot alerts and security updates, and private vulnerability reporting once the repository is public. Secret scanning and push protection are in `repository.tf`, on when `visibility` is `public` |
| `imports.tf` | Import blocks that adopt the live repository into a fresh state |

Not managed here: anything committed to the repository (`.github/`).

## The release token

The release workflow publishes the GitHub release and pushes the formula to
`eyelock/homebrew-tap` with `RELEASE_TOKEN`, a fine-grained token limited to those two
repositories with **Contents: Read and write**. GitHub never returns its value, so Terraform only
tracks that the secret exists. Set or rotate it with gh, so the value never passes through
Terraform:

```bash
gh secret set RELEASE_TOKEN -R eyelock/ynr
```

`imports.tf` adopts it into state on the next apply.

## Use

Needs Terraform 1.10 or later, a GitHub token with the `repo` and `workflow` scopes (the repository's rulesets and security settings need admin on it), the `gh` CLI on `PATH` (private vulnerability reporting), and the
`ynr-terraform` AWS profile. State is in S3 at
`s3://ynr-terraform-state.eyelock.net/github/terraform.tfstate`, locked with a `.tflock` object
beside it; the bucket comes from [`../terraform-state`](../terraform-state/README.md). It holds no
secrets, and if it is ever lost the import blocks rebuild it from the live repository.

```bash
cd infra/github
export AWS_PROFILE=ynr-terraform
export GITHUB_TOKEN="$(gh auth token)"
terraform init
terraform plan     # no changes means the repository matches this configuration
terraform apply
```

To change a setting, edit the `.tf` file, `plan`, then `apply`. A change made in the GitHub UI
shows as drift in the next `plan`; either copy it into the configuration or `apply` to undo it.

The repository has `prevent_destroy` and `archive_on_destroy`, so `terraform destroy` stops, and
removing that guard archives the repository rather than deleting it.

## Set up from nothing

For a new owner or name, set `owner` and `repository`, then:

1. Delete `imports.tf`: there is nothing to import.
2. Create only the repository: `terraform apply -target=github_repository.ynr`.
3. Push `main` and `develop` from a clone. The protection and the default branch need them to exist.
4. Apply the rest: `terraform apply`.
