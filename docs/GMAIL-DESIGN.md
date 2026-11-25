# Gmail access: design note

Status: proposal. The unify-vs-separate choice awaits the owner.

## How calendar linking works today
- The OAuth client (Desktop or Web) is pasted in Settings or built in (`internal/google/auth.go`).
- A brain signs in through the bridge. The token is `<credsdir>/google_calendar_token_<brain>.json`, read only by the bridge; the sandbox denies brains that folder.
- Brains use `fridolin calendar ...` (`internal/toolcli/calendar_cmd.go`) against the bridge's HTTP API.
- Writes pass `calendarWrites` (`internal/app/calendar_writes.go`): lockdown, run risk >= write, owner approval (`useApproval`), audit line.
- Contacts already has its own sign-in and token (`google_contacts_token_<brain>.json`) via `StartSignInFor`.

## Unify or separate
- **A. Unified**: one token with calendar + Gmail scopes, incremental consent. One sign-in, but re-linking replaces the calendar token, `missingScopes`/health must become per-feature, and unticking Gmail on the consent screen could break calendar health.
- **B. Separate (recommended)**: `google_gmail_token_<brain>.json`, the Contacts pattern. Calendar tokens are untouched by construction; Gmail disconnects/revokes alone. Costs a second consent click per brain. The dashboard can show one "Google account" card with Calendar/Contacts/Gmail toggles; merging tokens later would not change the `fridolin mail` commands.

## Scopes
`gmail.modify` + `gmail.send`. Not `gmail.compose` (cannot read), not `mail.google.com` (adds permanent delete). Both are restricted scopes.

## Plan after the answer
1. `internal/google/gmail.go`: scopes, token, sign-in, API client, MIME builder (RFC 2047, In-Reply-To/References).
2. HTTP routes + a write guard like `calendarWrites`: `send` needs owner approval; label/archive/mark-read need write risk.
3. `fridolin mail list/search/read/attachment/draft/send/label/archive/mark-read`, JSON output.
4. Inbound mail marked untrusted in all output.
5. Docs (Cloud Console steps, brain-prompt snippet) and tests.

## Cloud Console steps for the owner
Use the same project as the calendar client. Settings → Google sign-in shows its project ID; the repo does not record it.
1. Enable the Gmail API: https://console.cloud.google.com/flows/enableapi?apiid=gmail.googleapis.com
2. Data access (https://console.cloud.google.com/auth/scopes) → manually add
   `https://www.googleapis.com/auth/gmail.modify` and `https://www.googleapis.com/auth/gmail.send`. Keep the existing calendar/drive scopes.
3. Audience (https://console.cloud.google.com/auth/audience): if *Testing*, add the personal brain's Google account as a test user; Google ends sign-ins after 7 days. If *In production* (unverified), sign in via Advanced → Go to Fridolin (unsafe); the 100-account cap still applies.
4. The existing Desktop client needs no change.
5. After the code ships: link Gmail for the personal brain in the dashboard (medorg stays unlinked).
