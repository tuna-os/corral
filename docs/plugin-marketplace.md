# Publishing Corral plugins

Corral plugins are standalone `corral-<name>` executables. The marketplace is
discovery and verified delivery; it does not load third-party code into the
main Corral process.

## Authoring contract

- Name the executable `corral-<name>`, where the name matches
  `^[a-z][a-z0-9-]{0,62}$`.
- Take normal arguments directly from the command line. Corral connects stdin/stdout/stderr and
  returns the plugin process's exit status. Human output belongs on stdout;
  diagnostics belong on stderr.
- Read `CORRAL_PLUGIN` when behavior depends on the dispatched name. Honor the
  same `CORRAL_KUBE_CONTEXT`, `CORRAL_INCUS_REMOTE`, and
  `CORRAL_LIBVIRT_URI` one-shot context variables as core commands. Never
  switch kubectl, Incus, or libvirt global state.
- Support `--corral-plugin-metadata`. It must print a single JSON object with
  API `corral.plugin/v1`, then exit successfully and do no other work. Go
  plugins can call `sdk.HandleMetadata` before they construct their command.
- Treat capabilities and permissions as declarations, not authorization.
  Corral asks for install consent; the plugin must still use least-privilege
  credentials and rely on Kubernetes, filesystem, and network enforcement.
- Declare `supportedBackends` using `qemu`, `kubevirt`, `incus`, `libvirt`, or
  `all`. Reject a selected backend before mutation when it is not supported;
  do not silently fall back to another context.

Minimal Go entry point:

```go
package main

import (
    "fmt"
    "os"

    "github.com/tuna-os/corral/pkg/plugin/sdk"
)

func main() {
    if sdk.HandleMetadata(sdk.Metadata{
        Name: "hello", Version: "1.0.0",
        Capabilities: []string{"cli-command"},
        Permissions: []string{"read Corral configuration"},
        SupportedBackends: []string{"all"},
    }) { return }
    fmt.Fprintln(os.Stdout, "hello from Corral")
}
```

Test the binary directly, test the metadata handshake, then test dispatch with
`CORRAL_PLUGIN_DIR=/path/to/bin corral hello`. Plugins should not import
`cmd`; reusable behavior for a backend belongs in a `pkg/` package.

## Adding a screen or a widget

A plugin can add widgets to the Datacenter dashboard and screens of its own.
It does not send markup or script. It sends data, and corral draws it. The
design is in [RFC-0002](rfc/0002-plugin-ui-contributions.md).

Add a `ui` section to the metadata:

```go
sdk.Metadata{
    Name: "aws-cost", Version: "1.0.0",
    SupportedBackends: []string{"all"},
    UI: &sdk.UI{
        Widgets:  []sdk.UIItem{{ID: "spend", Title: "AWS spend", Command: "ui spend", Refresh: "60s"}},
        Sections: []sdk.UIItem{{ID: "hosts", Title: "AWS hosts", Icon: "server", Command: "ui hosts"}},
    },
}
```

For each item, corral runs the plugin with the item's `command` as arguments,
with no shell. The plugin then writes one document to stdout:

```json
{ "kind": "rows", "rows": [ { "label": "This month", "value": "$41.20", "state": "ok" } ] }
{ "kind": "table", "columns": ["Host", "State"], "rows": [["i-0abc", "stopped"]] }
{ "kind": "message", "text": "No instances carry the corral tag." }
```

Rules:

- `state` is `ok`, `warn`, `bad` or `muted`, or empty.
- Corral refuses an unknown kind, an unknown field and output over the limits
  in `sdk` (`MaxDocumentBytes`, `MaxDocumentRows`, `MaxDocumentCols`,
  `MaxDocumentString`, `MaxDocumentText`). Test your output with
  `sdk.ParseDocument`.
- Corral shows every string as text. Markup in a value appears as written.
- A call stops after 10 seconds. A failure shows your stderr to the operator,
  so write a useful line there.
- Corral asks for a document at most every `sdk.MinUIRefresh` (15 seconds),
  whatever `refresh` says. All viewers share one answer.
- A document is read-only. A plugin cannot add a button that acts on the fleet.

Widgets wait in "Add widget" on the Datacenter dashboard. Sections open from
the Extensions screen and the command palette, not from the sidebar tree.

Put the same `ui` section in the marketplace entry. The Extensions screen then
shows what the plugin adds before the operator installs it. A plugin whose only
purpose is a screen is a valid entry: declare `"supportedBackends": ["all"]`.

## External marketplace

1. Build immutable binaries for each advertised platform.
2. Publish them at HTTPS URLs that you never replace.
3. Create an index that conforms to `marketplace/schema-v2.json`. Every artifact
   needs its SHA-256 digest. Publishers may also sign the lowercase
   digest with Ed25519 and publish the base64 public key.
4. Validate it with `corral marketplace validate index.json`.
5. Host the index over HTTPS and test it with
   `corral marketplace add <source> <url>`.

Corral accepts only an index that declares
`"schemaVersion": "corral.marketplace/v2"`. Corral rejects an index without
that declaration, because it has no publisher, no license, and no digest. An
operator who accepts that danger can add the source with
`corral marketplace add <source> <url> --allow-unverified`. Corral then prints
a warning when it reads the index and again at install time, and
`corral marketplace list` shows the source as `unverified`.

To select a plugin with a duplicate name, use `source/name`. Installation
shows the publisher and the requested permissions, and it needs explicit
consent to those permissions. Corral verifies compatibility, digest, and any
supplied signature before it atomically replaces an installed plugin; failed state writes roll the
binary back.

## Recommended CI

Build from a version tag, run the plugin's tests, generate checksums, validate
the index, and publish both artifacts and index in one immutable release. The
repository's `scripts/generate-marketplace.sh` and `publish-plugins` workflow
are a reference that works.

## Signed plugins and SBOMs

CI signs each first-party plugin binary with Sigstore. It uses the GitHub
OIDC token of the `publish-plugins` job, so it stores no private key. CI also
makes an SPDX SBOM and a signed attestation that binds the SBOM to the binary.
The immutable `plugins-<commit>` release holds these files next to each
binary:

| File | Content |
|---|---|
| `corral-NAME-linux-ARCH.sigstore.json` | Signature bundle for the binary |
| `corral-NAME-linux-ARCH.spdx.json` | SPDX JSON SBOM |
| `corral-NAME-linux-ARCH.sbom.sigstore.json` | Signed attestation of the SBOM |

### What Corral checks at install time

Corral has the first-party signer identity in its code. When you install a
plugin from the built-in marketplace, Corral does these checks after the
SHA-256 check:

1. It verifies the `.sigstore.json` bundle. The certificate must have the
   exact identity and issuer. The bundle must agree with the digest of the
   downloaded binary.
2. It verifies the `.sbom.sigstore.json` attestation. The signer must be the
   same. The attestation must name the digest of the binary, and its
   predicate must be an SPDX document.

If a check fails, or if a bundle is missing, Corral does not install the
plugin. If the checks pass, Corral writes the signer and the SBOM digest to
the install record. It keeps the SBOM at
`<plugin dir>/.installed/NAME.spdx.json`. `corral plugin info NAME` shows
the signer.

Corral gets the Sigstore trusted root through TUF, and it keeps a cache in
the user cache directory. A host without access to the internet can use a
local copy: set `CORRAL_SIGSTORE_TRUSTED_ROOT` to the path of a
`trusted_root.json` file.

### Signatures from your own marketplace

To make Corral need signatures from a different marketplace, give the
identity of the signer when you add the source:

```sh
corral marketplace add acme https://plugins.example.com/index.json \
  --signer-identity https://github.com/acme/plugins/.github/workflows/release.yml@refs/heads/main \
  --signer-issuer https://token.actions.githubusercontent.com
```

Publish the two bundles next to each artifact URL, with the suffixes in the
table above. The `publish-plugins` job in `.github/workflows/ci.yml` shows
the `cosign` commands. `corral marketplace list` shows a source with a
signer as `signed`. The identity in the index has no effect. Only the
operator and the code of Corral can set the signer.

### Verify by hand

To verify a binary without Corral, download it and its bundles. Then run
these commands with [Cosign](https://docs.sigstore.dev/cosign/) 3.0 or later:

```sh
f=corral-bootc-linux-amd64
id=https://github.com/tuna-os/corral/.github/workflows/ci.yml@refs/heads/main
iss=https://token.actions.githubusercontent.com
cosign verify-blob --bundle "$f.sigstore.json" \
  --certificate-identity "$id" --certificate-oidc-issuer "$iss" "$f"
cosign verify-blob-attestation --bundle "$f.sbom.sigstore.json" --type spdxjson \
  --certificate-identity "$id" --certificate-oidc-issuer "$iss" "$f"
```

Use the exact identity. Do not use `--certificate-identity-regexp`, because
a workflow in a fork can match a loose expression.
[enterprise-readiness.md](enterprise-readiness.md) gives the full policy.

## First-party plugins

[first-party-plugins.md](first-party-plugins.md) documents the maintained
catalog and its maturity. First-party means published
by tuna-os through the curated marketplace; it does not grant the plugin more
runtime privilege than an external plugin.
