# What the redactor recognises

Coral removes secrets from a board reply, a stream or an event before it leaves this computer for a Coral Hub viewer, and from the lines of `coral.log`
(Settings > Coral Hub, "Hide tokens from view-only hub users"). The replacement is `[redacted token]`. Three sources of rules run together,
so a library update cannot take Coral's own away, and the text is checked a chunk at a time with a time budget: text that could not be checked in time is
never passed on as it is.

1. **betterleaks' embedded rules** (pinned: `RulesVersion` in `internal/secrets`, today betterleaks v1.9.0): provider tokens and keys in their own formats,
   private keys, and generic rules for a value assigned to a secret-looking name. **Many of these rules only redact a value that looks random enough**
   (see the table below). A value under a rule's entropy threshold stays showing, however well the pattern matched.
2. **Coral's own provider rules** (`internal/secrets/extra.go`): Anthropic and OpenAI keys of any length, AWS access key ids, bearer, basic and JWT values,
   the `aws_secret_access_key` value, the password of a URL's credentials (the part after the user name and its colon). A PEM private key is redacted from its header line to its end line (the library's rule drops a block whose base64 holds "false" in any case, #5158).
3. **Coral's keyword rules** (`internal/secrets/keyword_rules.go`, #5132, #5141): **the value after a recognised keyword is redacted whatever its entropy.**
   The keyword is the evidence, so the value need not look random. **These run only for Coral Hub** (the cloud tunnel's redactor, `secrets.HubRedactor`): they
   cost redaction speed (about three times the time on text dense in names such as key and token), so every local caller (`coral.log` lines, events, saved
   views, the board) keeps rules 1 and 2 only (`secrets.Redactor`).

### Where the hub's rules (the keyword rules too) apply

- Everything the cloud tunnel sends (replies, streams, terminal frames): `secrets.HubRedactor`.
- A request that came through the tunnel, for the text it cuts or keeps before the reply is redacted: a chat page's trimmed tool fields (`?trim=N`) and a saved
  message's snippet (`requestRedactor`). Local requests keep the local rules.
- What a hub viewer matches a team-artifact query on (`shownText`): it must be what the reply shows.
- A file or a message sent to another Coral through Coral Hub, which the hub does not redact: the scan before it leaves (`secrets.HubBudgetedRedactor`).
  With the keyword rules on, a file or message with a keyword and a value in it is refused. The refusal says the line (in the file, or in the re-encoded copy that found it) and the rule, never any part of the secret; in this scan only, a value that is a code reference (letters and underscores joined by dots, no digit: `config.PostHogKey`) is not taken for a secret.

The local callers (`coral.log`, events, the board, saved views of local requests) keep `secrets.Redactor`: they cannot reach a hub viewer without the reply's own redaction.

### What the keyword rules cost

They are regular expressions, so they only run where a name could be: `internal/secrets/keyword_gate.go` finds each `=`, `:`, `%3D` or full-width separator
whose word before it ends like a name (key, token, secret, password, ...), and each `--flag` of that kind, and gives the rules that name and its value
(its line to the end of the value), not the text around. `keyword_gate_test.go` holds the result byte-equal to running the rules over all of the text,
and `TestKeywordPlacesAreTight` holds the places to a few percent of ordinary text. Through the hub that costs about 10 percent of the speed of the base rules on
source-code-heavy terminal text and on transcript JSON (Mac, `Redactor` against `HubRedactor`), and about 8 percent on a 2 MiB reply; text that is nothing but names
costs more (about a third of the base speed).

### Turning the keyword rules off

They are on whenever Coral is connected to Coral Hub. Two switches, both local: the setting `hub_redact_keywords` (Settings > Coral Hub, or `PUT /api/settings`
with `{"hub_redact_keywords": false}`; read on every reply) and the environment variable `CORAL_REDACT_KEYWORDS=off` (also `0`, `no`, `false`; read at every
reply). Off, the hub's redactor is rules 1 and 2 only, and so is the scan of what is sent to another Coral. Coral Hub cannot change either: `PUT /api/settings` is never reachable through the tunnel.

## The keyword rules

| Shape | Examples | Value |
|---|---|---|
| a key, token or secret named by its keyword | `api_key=`, `apikey:`, `x-api-key:`, `access_token=`, `GITHUB_TOKEN=`, `client_secret=`, `aws_secret_access_key =`, `"token": "..."`, any prefix (`DEPLOY_TOKEN`) | 16 or more characters (`token`, `key`, `access_key`, `api_secret`, ...), 8 or more after `secret` |
| a password | `password=`, `passwd:`, `DB_PASSWORD=`, `--password=`, `"passphrase": "..."` | 8 or more characters; it may hold `&`, `;` and `,` (the value runs to a space or a quote) |
| a key in a URL's query string | `?api_key=...`, `&key=...&`, `?token=`, `&sig=`, `&secret=` | 8 or more characters, up to `&` or a space |
| an upper-case environment variable | `export STRIPE_KEY=...`, a line `SSH_KEY=...` of an env dump | 16 or more characters |
| curl with credentials | `curl -u` or `curl --user` with a user name, a colon and a password | the password, 4 or more characters (a variable or a `<placeholder>` is left) |
| an Authorization header | `Authorization: Bearer <token>`, `Authorization: token <token>`, `Authorization: Basic <base64>` | 12 or more characters (bearer, token), whatever they look like |
| the same, as JSON inside a JSON string | a key, token or password whose quotes are escaped (once or twice) in a logged string | as above |
| the same, URL-encoded | `api_key%3D...`, `token%3D...`, also inside a `next=` or `redirect_uri=` URL, also encoded twice | 8 or more characters (12 for a query-string name), up to an encoded `&` or a quote |
| a name written as one word | `PGPASSWORD`, `MYSQL_PWD`, `SSHPASS`, `AWS_SECRET`, `SECRET_KEY_BASE`, `REDISCLI_AUTH`, `APITOKEN` (an exact list: `mytoken` is still not a name) | 8 or more characters |
| a command-line argument | the flags `--password`, `--token`, `--api-key`, `--db-password` followed by a value (after a space or `=`); the password after `mysql -p` (no space) or `sshpass -p` | as the name above (4 or more after `-p`); a value that starts with `-` is the next flag and is left |
| a Cookie or Set-Cookie header | every `name=value` pair of the header | 16 or more characters, or 8 or more under a name with `sess`, `sid`, `auth`, `token`, `key`, `secret`, `pass`, ... in it; the attributes (`Path`, `Domain`, `Expires`, `SameSite`, ...) are left |
| a value in markup | an XML element named like a secret (`<api_key>VALUE</api_key>`) and an HTML form field (`<input name="api_key" value="VALUE">`, either order of the attributes) | as the name above |
| a URL-encoded JSON pair | `%22api_key%22%3A%22VALUE%22`, encoded once or twice; `password%3D%20VALUE` | as the name above |
| quote entities and encoded name bytes | `&amp;quot;`, `&quot` with no semicolon, `api%5Fkey` and `x%2Dapi%2Dkey` are read as the quote and the underscore or hyphen they stand for | as the name above |
| curl's cookie argument and docker login | the cookie string of `curl -b` (glued on, quoted or not) and `--cookie`; the password argument of `docker login` (`-p` or `--password`) | a cookie value as for a Cookie header; a docker password of 4 or more characters |
| URL-encoded colon | a name followed by an encoded colon and an encoded space or a plus (`%3A%20`, `%3A+`), once or twice encoded | as the encoded rules above |
| HTML-entity quotes | the quotes around a name and its value written as `&quot;`, `&#34;`, `&#x22;`, `&apos;`, `&#39;` | as the name above; a closing entity is not part of the value |
| the full-width equals sign and colon (U+FF1D, U+FF1A) | read as `=` and `:` | as above |

The tradeoffs are on purpose, to keep ordinary text readable:

- A **name** follows `_` or `-` or starts the word, and is never inside one: `DB_PASSWORD`, `x-api-key` and `api_key` match; `mytoken`, `secretary` and `tokens=5` do not.
- A **short value** is left alone: `token=abc` and `password=` in prose, and **a password of 7 characters or fewer** after `password=`, are not redacted by these rules
  (betterleaks may still). Keys and tokens are longer than 16 characters in every provider's format.
- After an **unquoted `:`** or a bare space after a name (not after a `--flag`), a value made only of letters is kept unless it is 16 or more characters (a word such as "required" after "password:" reads as prose); after `=` or a quote any value is redacted.
- A value that is a **variable or a template** (`$TOKEN`, `${TOKEN}`, `{{ token }}`, `<token>`, `%(token)s`) or **one repeated character** (`****`) is not a value and is left showing.

`internal/secrets/keyword_rules_test.go` draws 2000 random values per shape (fixed seed; hex, letters and digits, base64, base64url) for 29 shapes (query strings, headers, AWS, curl, exports,
assignments, Authorization lines); `keyword_rules2_test.go` does the same for 38 more (escaped JSON, URL-encoded, one-word names, command-line arguments, cookies, full-width separators) and requires **zero** that survive; a fuzz target and an adversarial timing test (linear in the text) go with it.

## Rules that are gated on entropy

A betterleaks rule with a threshold discards a finding whose Shannon entropy (bits per character over the secret group) is **at or below** it.
This is every embedded rule with one at v1.9.0 (no embedded rule uses the library's token-efficiency gate). A hex string of 32 characters has at most 4 bits per
character and a typical random one about 3.9, so the rules at 4.0 and 3.5 miss a share of random values: that is why the keyword rules above exist and do not use a threshold.
The table is checked against the library in `pkg/redact/betterleaks` (a library update that changes a threshold fails the test until this table is updated).

<!-- entropy-table:start -->
| Rule | Entropy threshold |
|---|---|
| `1password-secret-key` | 3.80 |
| `1password-service-account-token` | 4.00 |
| `aws-access-token` | 3.00 |
| `aws-amazon-bedrock-api-key-long-lived` | 3.00 |
| `aws-amazon-bedrock-api-key-short-lived` | 3.00 |
| `aws-secret-access-key` | 4.00 |
| `azure-ad-client-secret` | 3.00 |
| `azure-app-configuration-connection-string` | 3.50 |
| `azure-client-id` | 2.50 |
| `azure-servicebus-connection-string` | 3.00 |
| `azure-storage-account-key` | 4.00 |
| `azure-storage-account-name` | 1.50 |
| `azure-tenant-id` | 2.50 |
| `cerebras-api-key` | 3.00 |
| `clojars-api-token` | 2.00 |
| `curl-auth-header` | 2.75 |
| `curl-auth-user` | 2.00 |
| `cursor-api-key` | 3.50 |
| `deepseek-api-key` | 3.50 |
| `digitalocean-access-token` | 3.00 |
| `digitalocean-pat` | 3.00 |
| `doppler-api-token` | 2.00 |
| `duffel-api-token` | 2.00 |
| `dynatrace-api-token` | 4.00 |
| `easypost-api-token` | 2.00 |
| `easypost-test-api-token` | 2.00 |
| `elevenlabs-api-key` | 3.50 |
| `figma-personal-access-token` | 3.50 |
| `flutterwave-encryption-key` | 2.00 |
| `flutterwave-public-key` | 2.00 |
| `flutterwave-secret-key` | 2.00 |
| `flyio-access-token` | 4.00 |
| `gcp-api-key` | 4.00 |
| `gcp-gemini-api` | 4.00 |
| `generic-api-key` | 3.50 |
| `github-app-token` | 3.00 |
| `github-fine-grained-pat` | 3.00 |
| `github-oauth` | 3.00 |
| `github-pat` | 3.00 |
| `github-refresh-token` | 3.00 |
| `gitlab-cicd-job-token` | 3.00 |
| `gitlab-deploy-token` | 3.00 |
| `gitlab-feature-flag-client-token` | 3.00 |
| `gitlab-feed-token` | 3.00 |
| `gitlab-incoming-mail-address-token` | 3.00 |
| `gitlab-incoming-mail-token` | 3.00 |
| `gitlab-kubernetes-agent-token` | 3.00 |
| `gitlab-oauth-app-secret` | 3.00 |
| `gitlab-pat` | 3.00 |
| `gitlab-pat-routable` | 4.00 |
| `gitlab-pat-routable-versioned` | 4.00 |
| `gitlab-ptt` | 3.00 |
| `gitlab-rrt` | 3.00 |
| `gitlab-runner-authentication-token` | 3.00 |
| `gitlab-runner-authentication-token-routable` | 4.00 |
| `gitlab-scim-token` | 3.00 |
| `gitlab-session-cookie` | 3.00 |
| `groq-api-key` | 3.50 |
| `harness-api-key` | 3.40 |
| `hashicorp-tf-api-token` | 3.50 |
| `hashicorp-tf-password` | 2.00 |
| `heroku-api-key-v2` | 4.00 |
| `huggingface-access-token` | 2.00 |
| `huggingface-organization-api-token` | 2.00 |
| `infracost-api-token` | 3.00 |
| `intra42-client-secret` | 3.00 |
| `jwt` | 3.00 |
| `jwt-base64` | 2.00 |
| `kimi-api-key` | 3.50 |
| `maxmind-license-key` | 4.00 |
| `minimax-api-key` | 3.50 |
| `mongodb-atlas-service-account-id` | 3.00 |
| `mongodb-atlas-service-account-secret` | 3.00 |
| `mongodb-connection-string` | 4.00 |
| `notion-api-token` | 4.00 |
| `npm-access-token` | 2.00 |
| `nuget-config-password` | 1.00 |
| `nvidia-api-key` | 3.50 |
| `octopus-deploy-api-key` | 3.00 |
| `okta-access-token` | 4.00 |
| `ollama-api-key` | 3.50 |
| `openai-api-key` | 3.00 |
| `openrouter-api-key` | 3.50 |
| `openshift-user-token` | 3.50 |
| `ovh-application-key` | 2.00 |
| `ovh-application-secret` | 3.00 |
| `ovh-consumer-key` | 2.00 |
| `perplexity-api-key` | 4.00 |
| `planetscale-api-token` | 3.00 |
| `planetscale-id` | 3.00 |
| `planetscale-oauth-token` | 3.00 |
| `planetscale-password` | 3.00 |
| `polymarket-api-key` | 3.00 |
| `polymarket-api-secret` | 3.00 |
| `polymarket-private-key` | 3.50 |
| `posthog-personal-api-key` | 3.00 |
| `posthog-project-api-key` | 3.00 |
| `postman-api-token` | 3.00 |
| `prefect-api-token` | 2.00 |
| `pulumi-api-token` | 2.00 |
| `pypi-upload-token` | 3.00 |
| `readme-api-token` | 2.00 |
| `replicate-api-token` | 3.00 |
| `rubygems-api-token` | 2.00 |
| `scalingo-api-token` | 2.00 |
| `sendgrid-api-token` | 2.00 |
| `settlemint-application-access-token` | 3.00 |
| `settlemint-personal-access-token` | 3.00 |
| `settlemint-service-access-token` | 3.00 |
| `shippo-api-token` | 2.00 |
| `shopify-access-token` | 2.00 |
| `shopify-custom-access-token` | 2.00 |
| `shopify-private-app-access-token` | 2.00 |
| `shopify-shared-secret` | 2.00 |
| `slack-app-token` | 2.00 |
| `slack-bot-token` | 3.00 |
| `slack-config-access-token` | 2.00 |
| `slack-config-refresh-token` | 2.00 |
| `slack-legacy-bot-token` | 2.00 |
| `slack-legacy-token` | 2.00 |
| `slack-legacy-workspace-token` | 2.00 |
| `slack-session-cookie` | 3.50 |
| `slack-session-token` | 3.50 |
| `slack-user-token` | 2.00 |
| `sourcegraph-access-token` | 3.00 |
| `square-access-token` | 2.00 |
| `stability-ai-api-key` | 3.50 |
| `stripe-access-token` | 2.00 |
| `supabase-management-token` | 3.50 |
| `supabase-project-api-key` | 4.00 |
| `supabase-project-url` | 3.00 |
| `togetherai-api-key` | 3.00 |
| `twilio-api-key` | 3.00 |
| `vault-batch-token` | 4.00 |
| `vault-service-token` | 3.50 |
| `xai-api-key` | 3.50 |
| `zai-api-key` | 3.50 |
<!-- entropy-table:end -->
