# Infra

What ynr's own development needs outside the code: where Terraform keeps its state, and the
GitHub repository's settings.

| Path | What it is |
|---|---|
| [`terraform-state/`](terraform-state/README.md) | Terraform for the S3 bucket that holds the Terraform state for everything in this repository, and the IAM user that runs it. Applied once to bootstrap, then only to change the bucket or the user. |
| [`github/`](github/README.md) | Terraform for the `eyelock/ynr` repository: settings, branch protection, labels, Actions permissions. |

The Terraform for ynr's own storage (ADR-005's `infra/aws`) arrives with the S3 adapter in the
walking skeleton's third slice.
