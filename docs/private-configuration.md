# Private deployment configuration

PCR's source and example manifests are deployment-neutral. Neither the `pcr`
CLI nor Skipper's `/deploy` integration has a built-in PCR origin. Both must
be pointed at an installation explicitly; missing configuration must never
select a production service implicitly.

## What stays private

Keep real service origins, database addresses, identity-provider settings,
access groups, Slack workspace/channel IDs, and network selectors in a separate
private deployment repository. Inject credentials through its secret manager.
Do not put live tokens, app passwords, or exported event data in example files,
test fixtures, screenshots, or public build artifacts.

For workstation use, prefer configuration outside the checkout. PCR creates
its platform-default TOML file with mode `0600`; use `pcr config path` to find
it. In PCR's checkout, local `.env`/`.env.*` files (except the Git-allowed
`.env.example` template) and `private/` are excluded from Git and the Docker
build context as a convenience, not a security boundary. Ignoring
files does not protect files already tracked, copied elsewhere, or present in
Git history. Neither PCR nor Skipper's Go configuration loader automatically
reads these files; provide values through the process environment or PCR's
explicit config-file option.

## PCR CLI

Replace the example origin with the installation's real URL locally:

```bash
pcr --url https://changes.example.com config init
pcr config set-credential
pcr config show
```

URL precedence is `--url`, `PCR_URL`, then the TOML `url` field. Automation
should receive `PCR_URL` from private configuration and `PCR_CREDENTIAL` from
the secret manager. There is no compiled fallback. Existing TOML files with
an explicit URL continue to work; installations that previously relied on the
implicit destination must set it before upgrading the CLI.

## Skipper Slack integration

Skipper is an HTTP client of PCR; PCR does not import Skipper or require
organization-specific services, teams, topology, or channel names. Configure
these values in Skipper's private deployment environment:

| Setting | Value supplied privately |
|---|---|
| `SKIPPER_PCR_URL` | PCR HTTPS origin |
| `SKIPPER_PCR_CREDENTIAL` | Dedicated integration identity's Beyond email/app-password composite |
| `SKIPPER_DEPLOY_WORKSPACE_ID` | Authorized Slack workspace ID |
| `SKIPPER_DEPLOY_CHANNEL_IDS` | Comma-separated authorized channel IDs |

The credential, workspace, and channel allowlist enable `/deploy` together;
an enabled integration also requires the URL. Leaving those three unset keeps
deployment recording disabled. `/deploy` only records changes; it does not
execute a rollout or invoke status-page integrations. It records service,
environment, and optional owning team as user-supplied tags, with Slack IDs as
event attribution. Those operational records belong in the protected PCR
installation, not in the public source tree.

Register `/deploy` and enable Interactivity in the selected Slack app separately
when a rollout is approved. Do not start a local Socket Mode bot with the
production app tokens for isolation: it would share event delivery with the
production bot. The local functional suite uses fake Slack/PCR endpoints and
needs no live credentials or Slack app registration.

The CLI and Skipper adapter currently use Beyond's credential-authenticated
API path. GitHub, Google, Authentik, and Beyond remain selectable dashboard
providers. This cleanup changes deployment defaults and examples, not those
authentication protocols or the immutable event schema.

## Kubernetes and publication

The `k8s/` files are templates, not your production configuration. Copy or
overlay them in your private deployment repository; keep secret values in its
secret manager. Adapt the trusted-proxy NetworkPolicy selectors to the actual
workloads and retain the proxy-only network boundary. See
[deployment instructions](deployment.md).

Before publishing, review the exact staged files and any artifacts, screenshots,
and Git history being published for private data. Cleaning the current source
tree does not erase previously committed values. No history rewrite or live
deployment is part of this configuration separation.
