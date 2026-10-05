# Adopt the live repository into state. On a fresh state these import; once imported they are
# no-ops. To set the repository up from nothing (a new owner or name), delete this file first.
# The branches have no protection to import yet; the first apply creates it.

import {
  to = github_repository.ynr
  id = "ynr"
}

import {
  to = github_issue_labels.ynr
  id = "ynr"
}

import {
  to = github_actions_repository_permissions.ynr
  id = "ynr"
}

import {
  to = github_workflow_repository_permissions.ynr
  id = "ynr"
}

import {
  to = github_repository_vulnerability_alerts.ynr
  id = "ynr"
}

import {
  to = github_repository_dependabot_security_updates.ynr
  id = "ynr"
}
