resource "github_actions_repository_permissions" "ynr" {
  repository      = github_repository.ynr.name
  enabled         = true
  allowed_actions = "all"
}

# Workflows get a read-only GITHUB_TOKEN unless a job asks for more, and cannot approve PRs.
resource "github_workflow_repository_permissions" "ynr" {
  repository                       = github_repository.ynr.name
  default_workflow_permissions     = "read"
  can_approve_pull_request_reviews = false
}

# RELEASE_TOKEN publishes releases and the Homebrew formula (release.yml), as in ynf.
# GitHub never returns a secret's value, so Terraform owns that the secret exists and writes the
# value only when it creates it; after that the value is ignored. It is set with
# `gh secret set RELEASE_TOKEN -R eyelock/ynr` and adopted by the import in imports.tf, so the
# token never passes through Terraform or its state. To rotate it, set it again with gh.
data "github_actions_secrets" "ynr" {
  name = github_repository.ynr.name
}

resource "github_actions_secret" "release_token" {
  repository  = github_repository.ynr.name
  secret_name = "RELEASE_TOKEN"
  # The placeholder is never written: the precondition stops a create without a real value, and
  # ignore_changes stops an update.
  value = coalesce(var.release_token, "unset")

  lifecycle {
    ignore_changes = [value]

    precondition {
      condition     = var.release_token != null || contains(data.github_actions_secrets.ynr.secrets[*].name, "RELEASE_TOKEN")
      error_message = "RELEASE_TOKEN does not exist yet: set it with `gh secret set RELEASE_TOKEN -R eyelock/ynr` (see README.md), then plan again."
    }
  }
}
