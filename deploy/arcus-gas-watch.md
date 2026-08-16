# Arcus gas-watch configuration

`arcus-gas-watch.yml` reads two public, non-secret repository variables
through the GitHub Actions `vars` context:

- `ARCUS_WALLET_ADDRESS`: the active Arcus signer's 20-byte EVM address.
- `ARCUS_WALLET_EVER_FUNDED`: exactly `true` after deliberate operator
  funding, otherwise exactly `false`.

The active signer public key and the runtime's expected signer address are the
operational source of truth. The custody rotation must be recorded in the
private strategy tracker before this monitor is updated. The workflow does not
query KMS or the host for that value: doing so would add AWS/SSM privileges to
what is otherwise a public RPC read.

As of the custody rotation recorded on 2026-08-11, the active address is:

```text
0x812B6A6da8E0dF1fBCA7939ae32089Cf85c5DF05
```

## Initial configuration

The current wallet is deliberately funded, so configure both required values:

```bash
gh variable set ARCUS_WALLET_ADDRESS \
  --repo shigeo-nakamura/debot-dashboard \
  --body '0x812B6A6da8E0dF1fBCA7939ae32089Cf85c5DF05'
gh variable set ARCUS_WALLET_EVER_FUNDED \
  --repo shigeo-nakamura/debot-dashboard \
  --body 'true'
```

Repository-variable reads through `vars` require no Variables API request from
the runner. The job therefore grants no permissions to its `GITHUB_TOKEN`.
`PROJECTS_TOKEN` is exposed only to the reporting step that may create or update
the cross-repository incident.

An unset/malformed address or an unset value other than exact `true`/`false`
fails the job before the public RPC call. Missing configuration never silently
falls back to the retired wallet or to a never-funded state.

## Wallet rotation

1. Independently derive the EVM address from the new active signer public key
   and verify it matches the runtime's expected signer address.
2. Record the custody rotation in the private strategy tracker.
3. Deliberately fund the new address above the page threshold before making it
   active. Keep the old address funded until the cutover is verified.
4. In the same controlled change window, stop signed execution, update
   `ARCUS_WALLET_ADDRESS`, activate the matching runtime signer, and dispatch
   Arcus Gas Watch. Confirm the expected address and balance in the run summary
   before signed execution resumes or the old wallet is retired.
5. Keep/set `ARCUS_WALLET_EVER_FUNDED=true` for a pre-funded replacement. For a
   genuinely pre-launch wallet, use `false` until deliberate funding and change
   it to `true` immediately afterwards. Never infer this flag from an arbitrary
   positive balance because the public address can receive dust.

The repository-variable update and runtime cutover are not atomic. Pausing
signed execution and keeping both wallets adequately funded during that short
window avoids an unmonitored live signer or a false low-gas page.
