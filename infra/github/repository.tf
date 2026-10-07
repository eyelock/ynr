locals {
  public = var.visibility == "public"
}

resource "github_repository" "ynr" {
  name        = var.repository
  description = "Your named reporting: the factory's observation plane on OpenTelemetry. Spool, collector, storage and dashboards."
  visibility  = var.visibility
  topics = [
    "ai-agents",
    "duckdb",
    "factory",
    "golang",
    "observability",
    "opentelemetry",
  ]

  # The ADRs, on the default branch.
  homepage_url = "https://github.com/${var.owner}/${var.repository}/tree/develop/docs"

  has_issues      = true
  has_discussions = true
  has_projects    = true
  has_wiki        = false
  is_template     = false

  # Gitflow (branches.tf): feature PRs are squash-merged into develop; release and hotfix PRs into
  # main are true merges.
  allow_squash_merge          = true
  allow_merge_commit          = true
  allow_rebase_merge          = false
  allow_auto_merge            = false
  allow_update_branch         = true
  delete_branch_on_merge      = true
  squash_merge_commit_title   = "COMMIT_OR_PR_TITLE"
  squash_merge_commit_message = "COMMIT_MESSAGES"
  merge_commit_title          = "MERGE_MESSAGE"
  merge_commit_message        = "PR_TITLE"
  web_commit_signoff_required = false

  # A destroy archives the repository instead of deleting it.
  archive_on_destroy = true

  # Secret scanning and push protection only work once the repository is public (security.tf);
  # alerts and security updates are separate resources.
  security_and_analysis {
    secret_scanning {
      status = local.public ? "enabled" : "disabled"
    }
    secret_scanning_push_protection {
      status = local.public ? "enabled" : "disabled"
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

# A docs site (GitHub Pages from /docs on main, as ynf and ynm have) is added once ynr has docs
# beyond its ADRs; until then the homepage is the ADRs on develop.
