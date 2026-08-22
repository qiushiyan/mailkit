# Outlook: blocked on tenant consent

Status as of 2026-08-22. Not a technical failure -- an external approval sits
in front of the last step.

## Where it stands

| | |
|---|---|
| `m365` CLI | installed, v11.10.0 |
| Entra app registration `mailkit` | created in the planlab.ai tenant |
| Redirect URI + public client flows | configured |
| Delegated permissions | `Mail.Read`, `Mail.Send`, `email`, `User.Read` |
| Consent | **not granted** -- blocks everything |
| `OutlookBackend` | written, interface-aligned with Gmail, **never run** |
| `scripts/verify-outlook.py` | ready; five checks, one command |

Tenant: `125a5575-f4d7-4f2b-97b2-9c8f1fad37ca`

## The diagnosis, and why re-running login will not help

`m365 login` returns "Need admin approval". Two facts from the app's API
permissions page pin the cause:

- every permission shows **Admin consent required: No** -- the permissions
  themselves are user-consentable
- **"Grant admin consent for planlab.ai" is greyed out** -- this account holds
  no admin role in the tenant

Both true at once means the block is the **tenant's user-consent policy**, not
the permission set. Microsoft's default for newer tenants allows user consent
only for apps from a verified publisher; a self-registered app is not one.
Changing which permissions are requested cannot move this, and neither can
retrying the login.

Note this corrects an earlier claim in this project's notes: `Mail.Read` and
`Mail.Send` not requiring admin consent is a fact about the *permissions*, and
says nothing about whether the tenant will let a user consent at all.

## What to ask for

The tenant admin is the CTO (whoever created planlab.ai's Microsoft 365).
Any one of these unblocks it; the first is the smallest.

1. **Grant consent to this one app.** Entra ID -> App registrations ->
   `mailkit` -> API permissions -> "Grant admin consent for planlab.ai".
   Ten seconds, scoped to this app alone.
2. **Or the same thing by URL** --
   `https://login.microsoftonline.com/125a5575-f4d7-4f2b-97b2-9c8f1fad37ca/adminconsent?client_id=<APP_ID>`
3. **Or loosen the tenant policy** -- Enterprise applications -> Consent and
   permissions -> User consent settings -> "Allow user consent for apps".
   This affects every app in the tenant and should not be done for one tool.

Ask for 1. Mention 3 only if they raise it themselves.

### What the request should say

The thing an admin needs to know is the blast radius, and delegated permissions
are narrower than they sound:

> I registered an app called `mailkit` in our tenant. It is a command-line tool
> on my laptop that reads and sends **my own** mail -- the permissions are
> delegated (`Mail.Read`, `Mail.Send`), so it can only ever act as the signed-in
> user and only reach that user's mailbox. It cannot read anyone else's mail and
> it grants nothing tenant-wide. Our tenant requires admin consent for
> self-registered apps, so it needs one click from you:
> Entra ID -> App registrations -> mailkit -> API permissions ->
> "Grant admin consent". Happy to walk through what it does first.

Keep `Mail.ReadWrite` off the request. It was added at first and removed:
"read and write access to user mail" is a materially bigger ask than
"read user mail", and nothing here needs it.

## When consent lands

```
m365 login --appId <APP_ID> --tenant 125a5575-f4d7-4f2b-97b2-9c8f1fad37ca
python3 scripts/verify-outlook.py
```

The five checks and why each exists are in `operations.md`. Every one names a
failure that already happened once on Gmail, so treat a pass as evidence and a
skip as an open question.

## If consent is refused

The fallback is the local Mail.app store -- reading `.emlx` files and sending
via AppleScript, with no Azure involvement. It was measured and rejected as a
downgrade, not an equivalent: it needs Full Disk Access for the terminal, and
it only sees locally synced history rather than the full server-side mailbox.
It is a real option if Graph stays shut, and should be chosen knowingly.
