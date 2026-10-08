# Run ynr central on S3

Have collectors ship to a bucket, read the bucket from one place with `ynr central`, and put the
dashboard behind sign-in. The same steps work against MinIO for a local trial.

## What you need

- The full build of ynr on the machine that runs central ([Install ynr](install.md)). `ynr central`
  needs DuckDB, and the slim build refuses to start it.
- An S3 bucket, or a MinIO server, that the collectors can write to and central can read. For AWS,
  [Deploy the AWS infrastructure](deploy-the-aws-infrastructure.md) makes the bucket and one role
  for each collector and for central.
- AWS credentials for each process from the SDK's usual chain: the environment, a profile, or the
  role of the machine or job.

The store URL names the bucket and its region:

```text
s3://<bucket>?region=<region>
```

For an S3-compatible server add its address and path-style requests:
`s3://<bucket>?region=us-east-1&endpoint=http://127.0.0.1:9000&path_style=true`. A prefix goes after
the bucket, as `s3://<bucket>/<prefix>?region=...`.

## Ship from the collectors

On each collector, a runner pool or a host, point `ynr serve` at the bucket and give it the
collector's stable id. The id must match `^[a-z0-9][a-z0-9._-]{0,62}$` and must be the id its IAM
role is for:

```bash
ynr serve --store "s3://my-ynr-bucket?region=eu-west-2" --collector-id ci-pool-a
```

The slim build is enough for this. Add `--collector-instance <job id>` to record which job in the
pool a record came from; it is data, not identity.

## Run central

```bash
ynr central --store "s3://my-ynr-bucket?region=eu-west-2" --state /var/lib/ynr/central
```

Expected, on stderr:

```text
ynr: central on s3://my-ynr-bucket?region=eu-west-2: hot tier 6h0m0s, queries on /var/lib/ynr/central/central.sock, compacting as central@host/4182/9f3a1c20
```

Central keeps the last six hours in a hot tier in `--state` (change it with `--hot-window`), polls
the bucket every five seconds, compacts closed hours every five minutes, and answers `ynr query` on
the Unix socket the line names. It does not prune an S3 bucket: the bucket's lifecycle rules do.
Everything in `--state` can be deleted and rebuilt from the bucket. You can run two centrals; they
take a lease in the bucket before compacting an hour, so they never compact the same hour together.

Keep `ynr central` running under whatever supervises your processes: it runs in the foreground and
stops on Ctrl-C or `SIGTERM`.

## Ask it

On the same machine, name the socket:

```bash
ynr query runs --socket /var/lib/ynr/central/central.sock
```

`YNR_CENTRAL_SOCKET` sets the socket for `ynr query` too. When the state folder's path is too long
for a Unix socket, central puts the socket under the temporary directory and the startup line says
where; give it your own with `--socket` to avoid that. Without a socket, `ynr query` reads the
store named by `--store` or `YNR_STORE` directly.

## Try it against MinIO

MinIO publishes no free binaries, so build it from source at the release the CI uses:

```bash
GOBIN="$PWD/minio-bin" go install github.com/minio/minio@RELEASE.2025-10-15T17-29-55Z
mkdir -p minio-data/ynr-trial          # on a MinIO server, a folder in its data directory is a bucket
MINIO_ROOT_USER=ynrtest MINIO_ROOT_PASSWORD=ynrtestsecret \
  ./minio-bin/minio server minio-data --address 127.0.0.1:9000 --console-address 127.0.0.1:9001
```

In other terminals, with the same credentials in the environment:

```bash
export AWS_ACCESS_KEY_ID=ynrtest AWS_SECRET_ACCESS_KEY=ynrtestsecret
STORE='s3://ynr-trial?region=us-east-1&endpoint=http://127.0.0.1:9000&path_style=true'
ynr serve --store "$STORE" --collector-id laptop-a          # a collector
ynr central --store "$STORE" --state ./central --socket ./central.sock
ynr query runs --socket ./central.sock
```

Stop MinIO with Ctrl-C when you have finished.

## Serve the dashboard behind sign-in

`--ui` on central refuses to start unless sign-in is configured, so the dashboard is never served
unauthenticated. Register ynr with your OpenID Connect provider as a web application, with the
redirect URL it will be served at, ending in `/auth/callback`. Then:

```bash
export YNR_OIDC_CLIENT_SECRET=...           # the client secret
export YNR_SESSION_SECRET=...               # at least 32 bytes, so sessions survive a restart
ynr central --store "s3://my-ynr-bucket?region=eu-west-2" --state /var/lib/ynr/central \
  --ui 0.0.0.0:4320 \
  --oidc-issuer https://login.example.com \
  --oidc-client-id ynr-dashboard \
  --oidc-redirect-url https://ynr.example.com/auth/callback \
  --allow-domain example.com
```

Choose who may sign in with `--allow-emails a@example.com,b@example.com`, `--allow-domain`, or
`--allow-group` with `--group-claim` if the groups are not in a claim named `groups`. Any one rule
lets a person in; with none, nobody can sign in and central refuses to start. Without
`YNR_SESSION_SECRET` central makes a random one and says so: sessions then last only until it
restarts. Every setting is in [Sign-in (OIDC)](../reference/sign-in.md).

If central refuses, its message says why: a missing issuer, client id, client secret or redirect URL,
nobody allowed, a session secret shorter than 32 bytes, a redirect URL that does not end in
`/auth/callback`, or an `http` redirect URL that is not on loopback.

### Behind a reverse proxy

The dashboard still needs sign-in behind a proxy: the proxy adds TLS and a public name, and does not
replace it. Terminate TLS at the proxy, forward to `--ui`'s address, and pass the original `Host`
header unchanged. Central answers only the host in `--oidc-redirect-url` and rejects every other
`Host`, and it marks its cookies `Secure` when that URL is `https`. For nginx:

```nginx
server {
    listen 443 ssl;
    server_name ynr.example.com;
    location / {
        proxy_pass http://127.0.0.1:4320;
        proxy_set_header Host $host;
        proxy_buffering off;          # the live tail streams
    }
}
```

and start central with `--ui 127.0.0.1:4320` so only the proxy can reach it.
