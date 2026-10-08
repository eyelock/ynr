# Sign-in (OIDC)

The configuration of `ynr central --ui`, the central dashboard's sign-in
([ADR-005](../adr/005-storage-and-dashboards.md)). It implements OpenID Connect's authorization-code
flow with PKCE, in Go with `coreos/go-oidc`, after ynm's ADR-017. The local dashboard, `ynr serve
--ui`, has no sign-in and binds to loopback only.

## Settings

| Setting | Flag | Environment | Default |
|---|---|---|---|
| The dashboard's address | `--ui` | `YNR_UI` | off |
| Issuer URL | `--oidc-issuer` | `YNR_OIDC_ISSUER` | none |
| Client id | `--oidc-client-id` | `YNR_OIDC_CLIENT_ID` | none |
| Client secret | none | `YNR_OIDC_CLIENT_SECRET` | none |
| Redirect URL | `--oidc-redirect-url` | `YNR_OIDC_REDIRECT_URL` | none |
| Allowed emails | `--allow-emails` | `YNR_ALLOW_EMAILS` | none |
| Allowed email domain | `--allow-domain` | `YNR_ALLOW_DOMAIN` | none |
| Allowed group | `--allow-group` | `YNR_ALLOW_GROUP` | none |
| Group claim | `--group-claim` | `YNR_GROUP_CLAIM` | `groups` |
| Session secret | none | `YNR_SESSION_SECRET` | random, per start |
| Session length | `--session-ttl` | none | `8h` |

`ynr central --ui` refuses to start rather than serve data unauthenticated when any of these holds:

- the issuer, client id, client secret or redirect URL is missing;
- no allowed email, domain or group is given, because with none nobody could sign in;
- the session secret, if set, is shorter than 32 bytes;
- the redirect URL is not an `http` or `https` URL with a host, does not end in `/auth/callback`, or is
  `http` on a host that is not loopback;
- the issuer's discovery document cannot be fetched.

## The provider

Any provider that publishes an OpenID Connect discovery document at its issuer URL works. Register ynr
as a web application, with a confidential client, and:

- the redirect URL registered with the provider is exactly `--oidc-redirect-url`;
- the scopes requested are `openid`, `email` and `profile`;
- for `--allow-group`, the ID token carries the group names in the claim `--group-claim` names, as
  a list of strings or a single string.

## Who may sign in

Any one rule is enough.

| Rule | A person may sign in when |
|---|---|
| `--allow-emails` | their ID token's `email` is in the list, compared without case, and `email_verified` is true |
| `--allow-domain` | their verified `email` ends in `@<domain>` |
| `--allow-group` | the group is among the values of the claim `--group-claim` names |

## Paths

| Path | Does |
|---|---|
| `/auth/login` | Starts sign-in: sets a short-lived signed cookie with the state, nonce and PKCE verifier, and redirects to the provider. `?next=` names a local path to return to. |
| `/auth/callback` | Finishes sign-in, checks the state, the ID token and the nonce, applies the allow rules, and sets the session cookie. |
| `/auth/logout` | `POST` only. Clears the session and redirects to `/auth/signed-out`. |
| `/auth/signed-out` | A page that says you are signed out. |

Every other path needs a session. A browser navigation without one is redirected to `/auth/login`;
anything else (assets, the live tail stream, htmx fragments) gets 401.

## The session

A signed cookie named `ynr_session`, `HttpOnly`, `SameSite=Lax`, and `Secure` when the redirect URL is
`https`. It holds a handle and an end time, never an email or a name. The handle is `u` followed by 16
base32 characters of a hash of the issuer and the subject, as ynm ADR-017 names a person. The cookie
lasts `--session-ttl`. The login cookie, `ynr_login`, lasts 10 minutes and is single use.

Cookies are signed with `YNR_SESSION_SECRET`. Without it, central makes a random secret and says so
on stderr, and every session ends when central restarts.

## The Host

Central answers only requests whose `Host` is the host in the redirect URL (the scheme's default port
is ignored), so a page elsewhere cannot reach the dashboard by pointing its own name at it. Other hosts
get `421`. Behind a reverse proxy, pass the original `Host` through.

## Authorisation

Every signed-in person sees every lane, repository and handle. Authorisation by team is an open
question in [ADR-005](../adr/005-storage-and-dashboards.md).
