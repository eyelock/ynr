# Gitflow, as in ynh, ynm and ynf: develop is the default branch and takes feature PRs; main moves
# only by release or hotfix PRs and carries the release tags. Neither takes a direct push. The
# branches themselves are git history, pushed from a clone, not created here.
#
# Each branch has one repository ruleset and no classic branch protection, so there is one list of
# required checks to keep. See .github/BRANCH_PROTECTION.md.

resource "github_branch_default" "develop" {
  repository = github_repository.ynr.name
  branch     = "develop"
}

locals {
  # Rulesets by branch, with the status checks each requires. "All Clear" is the last job of the ci
  # workflow, which needs every other job; "Verify PR source branch" (protect-main.yml) runs only on
  # PRs into main, so it takes only develop, release/* and hotfix/*.
  rulesets = {
    develop = {
      name   = "Develop Branch Protection"
      checks = ["All Clear"]
    }
    main = {
      name   = "Main Branch Protection"
      checks = ["All Clear", "Verify PR source branch"]
    }
  }
}

resource "github_repository_ruleset" "this" {
  for_each = local.rulesets

  repository  = github_repository.ynr.name
  name        = each.value.name
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/${each.key}"]
      exclude = []
    }
  }

  # Repository admins (role 5) can bypass in an emergency.
  bypass_actors {
    actor_id    = 5
    actor_type  = "RepositoryRole"
    bypass_mode = "always"
  }

  rules {
    deletion         = true
    non_fast_forward = true

    # A pull request is required, with no approving review but every conversation resolved.
    pull_request {
      required_approving_review_count   = 0
      dismiss_stale_reviews_on_push     = false
      require_code_owner_review         = false
      require_last_push_approval        = false
      required_review_thread_resolution = true
    }

    required_status_checks {
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = false

      dynamic "required_check" {
        for_each = each.value.checks
        content {
          context = required_check.value
        }
      }
    }
  }
}
