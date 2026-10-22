# Client testing checklist

galdav's automated tests exercise the server through its own HTTP interface.
They cannot tell whether DAVx5, Apple's Calendar and Contacts, or Thunderbird
accept what galdav sends. This checklist is how to find out: a repeatable set
of scenarios, identical test data for every client, and a way to capture
evidence when something fails.

Use it before relying on galdav for real data, and again after changes to
discovery, properties, scheduling or sharing. Results go into the table in
[section 4](#4-record-the-results); anything that fails becomes an issue with the
evidence described in [section 3](#3-capturing-evidence).

Statements about what a client "should" do come from the protocols and from
what these clients are generally known to do, not from observation against
galdav. **Where reality differs, reality wins:** note it and report it.

Contents: [1 Preparation](#1-preparation) · [2 Scenarios](#2-scenarios) ·
[3 Evidence](#3-capturing-evidence) · [4 Results](#4-record-the-results) ·
[5 Client notes](#5-client-specific-notes) · [6 Triage](#6-triage-map)

---

## 1. Preparation

### Test server

- A galdav instance behind real TLS (iOS and macOS refuse plain HTTP and
  self-signed certificates; Let's Encrypt via Caddy is the easy route). Follow
  [deployment.md](deployment.md). A throwaway database is fine; do **not** test
  on data you care about.
- `realm` chosen once, `proxy.trusted` correct (so you can also test the admin
  UI through the proxy).
- Optional: `smtp` configured against a mailbox you can read, for the
  external-attendee scenarios (I8, I9).

### Accounts

Create in the admin UI (or `galdav useradd`), each **with an email address**:

| User | Email | Role |
|---|---|---|
| `alice` | `alice@yourdomain` | main test account, administrator |
| `bob` | `bob@yourdomain` | second local user: invitations, sharing, free-busy |

For the external-attendee scenarios invite a third address that has **no**
account, e.g. `carol@yourdomain-or-any-mailbox-you-own`.

Ideally use **two different clients at once**, one per user, so one user's
edits arrive at the other (for example alice on iOS and bob on Thunderbird).

### Test data

Every client must see the same data. Seed alice's account with:

```sh
docs/client-testing/seed.sh https://cal.example.com alice      # prompts for the password
```

It creates the calendar **CT Events**, the task list **CT Tasks** and the
address book **CT Contacts**, with dates relative to today (needs bash, curl and
GNU date). Re-running it is safe; `--clean` removes the three collections.

| Object | What it tests |
|---|---|
| `allday.ics` | all-day event spanning two days, transparent |
| `weekly-berlin.ics` | weekly series in `Europe/Berlin` with one deleted occurrence (`EXDATE`) and one moved to 11:00 (override) |
| `newyork.ics` | event in `America/New_York` (15:00 there) |
| `floating.ics` | no time zone at all |
| `rich.ics` | unicode, escapes, long folded description, alarm, location, URL, category, class |
| `todo.ics`, `todo-undated.ics` | tasks with and without due date |
| `contact-basic.vcf`, `contact-unicode.vcf`, `contact-v4.vcf`, `contact-photo.vcf` | vCard 3.0 basics, unicode and folded note, vCard 4.0, embedded photo |

Run it again for bob (`seed.sh … bob`) when a scenario needs bob to have data.

### Tools

- **Server checks.** Two shell helpers save a lot of typing; set the variables
  to your own values:

  ```sh
  export BASE=https://cal.example.com PW='alice-password'
  dav()  { curl -s -u "alice:$PW" "$@"; }                 # e.g. dav -X PROPFIND -H 'Depth: 1' $BASE/dav/calendars/alice/
  q()    { mysql galdav -e "$1"; }                         # run on the database host
  ```

  Useful queries: what a client stored,

  ```sql
  SELECT c.slug, o.uri, o.etag, o.component, o.updated_at
    FROM objects o JOIN collections c ON c.id = o.collection_id ORDER BY o.updated_at DESC LIMIT 20;
  SELECT CONVERT(data USING utf8mb4) FROM objects WHERE uri = 'THE-URI.ics'\G
  SELECT * FROM changes ORDER BY id DESC LIMIT 20;          -- the sync log
  ```

- **Traffic capture** (see [section 3](#3-capturing-evidence)).
- A note of each client's **name and exact version** and the OS version.

### Quick server smoke test (before involving any client)

```sh
curl -s $BASE/healthz                                               # ok
curl -sI $BASE/.well-known/caldav | head -3                         # 301, Location: /dav/
curl -sI $BASE/.well-known/carddav | head -3
dav -X PROPFIND -H 'Depth: 0' --data '<propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>' $BASE/dav/
dav -X PROPFIND -H 'Depth: 1' --data '<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>' \
    $BASE/dav/calendars/alice/ | grep -o '<D:href>[^<]*</D:href>'   # …/ct-events/, ct-tasks/, inbox/, outbox/
```

If these fail, fix the deployment first; clients will only add confusion.

---

## 2. Scenarios

Scenario IDs are used in the result table. For each, "Client" is what you
should see on the device; "Server" is how to confirm what galdav holds. Run
each scenario in every client the matrix in [5](#5-client-specific-notes) marks
as applicable. If a client has no UI for something, mark it *n/a*, not *fail*.

### D. Account setup and discovery

| ID | Steps | Expect |
|---|---|---|
| **D1** | Add the account using the host name only (iOS/macOS) or the base URL `…/dav/` (DAVx5). | Setup completes without errors or certificate warnings; no manual path fiddling. |
| **D2** | After setup, list the calendars and address books. | CT Events, CT Tasks (task list/reminders) and CT Contacts appear with the right names and colours (CT Events blue, CT Tasks red). `inbox` and `outbox` are **not** shown as ordinary calendars. |
| **D3** | Enter a wrong password during setup. | Clear authentication error, no hang, no account half-created. |
| **D4** | Remove the account and add it again. | Same result as D1/D2; nothing duplicated. |

### R. Reading the fixtures

Open the next two weeks and compare with the table in section 1. All times are
shown in the **device's** time zone, so convert when checking.

| ID | Check | Expect |
|---|---|---|
| **R1** | `allday.ics` | an all-day event over two consecutive days, **not** shifted by a day, shown as free/transparent if the client exposes that. |
| **R2** | `weekly-berlin.ics` | ten weekly occurrences at 09:00 Berlin time; the third is **missing**; the second shows **11:00–12:00** with the title "moved to 11:00". Check across a daylight-saving change if the series crosses one. |
| **R3** | `newyork.ics` | 15:00 in New York = the right local time (e.g. 21:00 in Berlin in summer). |
| **R4** | `floating.ics` | 12:00–13:00 local, on the same clock time wherever the device is. |
| **R5** | `rich.ics` | title with é, ☕, 日本語 intact; location "Zürich, Bahnhofstraße 1"; description shows two lines then the long folded text **without** stray spaces or breaks at fold points; alarm 15 minutes before; category and URL present if the client shows them. |
| **R6** | `todo.ics`, `todo-undated.ics` (task clients only) | both tasks present; the first with a due date and high priority. |
| **R7** | contacts | Ada Lovelace (work email, mobile number, organisation); Zoë Müller-Åström (nickname 日本語, home address in Zürich, birthday 12 April 1985, folded note intact); Grace Hopper (vCard 4.0: email and `tel:` number); contact with a photo (a 1×1 pixel, so it only has to load without errors). |
| **R8** | Server: `dav -X PROPFIND` the calendar and compare the object count with what the client shows. | Same number of items. |

### W. Writing from the client

Create each item **in the client**, then check it on the server and on a second
device or client.

| ID | Steps | Expect |
|---|---|---|
| **W1** | Create a timed event with title, location, notes, alert. | Appears on the server (`q` query above); second client shows identical details. |
| **W2** | Create an all-day event. | Server copy has `DTSTART;VALUE=DATE`; second client shows it on the right day. |
| **W3** | Create a weekly recurring event; delete one occurrence; edit another occurrence only; then edit "this and future" (where offered). | Second client shows exactly those exceptions; the server object holds one UID with `EXDATE` and `RECURRENCE-ID` entries. |
| **W4** | Create an event in a non-local time zone (set the event's zone, not the device's). | Time shown correctly on both clients; the server copy carries a `TZID` and its `VTIMEZONE`. |
| **W5** | Edit an existing fixture event (change title and time). | Change appears on the second client; server `etag` changed; no duplicate created. |
| **W6** | Delete an event. | Gone on the second client and from `objects`; a deletion row is in `changes`. |
| **W7** | Create a task, set a due date, mark it complete, then uncomplete it. | `STATUS`/`COMPLETED` on the server follow; second client agrees. |
| **W8** | Move an event to another calendar (CT Events → another calendar you created). | Moves rather than duplicates (clients implement this as delete plus create). |

### C. Contacts

| ID | Steps | Expect |
|---|---|---|
| **C1** | Create a contact with several emails/phones, an address, a birthday and a note; add a photo from the device. | Stored on the server (vCard visible via `q`); second client shows everything. A real photo is several hundred KB and must be accepted (limit 10 MiB). |
| **C2** | Edit a fixture contact (change a phone number). | Updated everywhere, still one contact. |
| **C3** | Delete a contact. | Removed everywhere. |
| **C4** | Create a contact **group** if the client has groups. | Either syncs sensibly or is a documented client limitation; note which. |
| **C5** | Search contacts for "müller" (case/accents) on the client. | Client-side search works on synced data. (Server-side `addressbook-query` is covered by the automated tests; this just checks the client doesn't choke.) |

### Y. Sync behaviour

| ID | Steps | Expect |
|---|---|---|
| **Y1** | Change something in the admin UI or with `curl` (PUT an event). Trigger or wait for a client sync. | The change arrives without re-adding the account. Note how long it took (clients have their own polling intervals). |
| **Y2** | Airplane mode: edit three events and delete one on client A; edit a different event on client B (online). Reconnect A. | All edits merge; nothing lost or duplicated. |
| **Y3** | Edit the **same** event on both clients while A is offline; reconnect A. | The client handles the conflict (one version wins, no corruption, no endless sync loop). Record which version won. |
| **Y4** | Seed 300+ events (see the loop in section 6) and add a fresh client/account. | Initial sync finishes in reasonable time with the correct count. |
| **Y5** | In the DB, simulate a lost sync state: `DELETE FROM changes WHERE collection_id = <id>;`, then sync a client that already has the account. | Client recovers with a full resync (galdav answers `valid-sync-token` errors for stale tokens); no stuck error state. |
| **Y6** | Restart galdav while a client is syncing. | Client retries and completes; no data loss. |
| **Y7** | Stop the proxy for five minutes, edit on the client, start it again. | Client uploads its changes once reachable. |
| **Y8** | Force-refresh/"Reset synchronisation" in the client. | Everything redownloaded, no duplicates. |

### T. Tasks

Covered by R6 and W7; additionally:

| ID | Steps | Expect |
|---|---|---|
| **T1** | Create a task with a repeat rule and complete one instance. | Next instance appears (client behaviour); server object stays valid iCalendar. |
| **T2** | Sub-tasks / `RELATED-TO` (only if the client supports them). | Round-trips or is cleanly ignored. |

### I. Invitations (scheduling)

Needs two local users on two clients (alice = organizer, bob = attendee) and
for I8/I9 the external address. Use **timed events in the future.**

| ID | Steps | Expect |
|---|---|---|
| **I1** | As alice, create an event and invite bob (by his email address). Save. | No error. Alice's copy shows bob as invitee. Server: alice's object has `ATTENDEE…SCHEDULE-STATUS=1.2` for bob. |
| **I2** | On bob's client, wait for/trigger a sync. | The invitation appears: in bob's default calendar as "needs action"/tentative **and** (Apple) in the inbox/notification area. Server: bob's inbox holds an `itip-….ics` with `METHOD:REQUEST`. |
| **I3** | Bob accepts. | Alice's event shows bob as **accepted** (after her next sync). Server: alice's inbox has an `METHOD:REPLY` item; her object has `PARTSTAT=ACCEPTED;…SCHEDULE-STATUS=2.0` for bob. |
| **I4** | Repeat with a fresh event: bob **declines**; another: **tentative**. | Alice sees declined / tentative respectively. A declined event no longer counts as busy time for bob (F2). |
| **I5** | Alice changes the time of the event (this raises `SEQUENCE` in most clients). | Bob's copy moves to the new time and returns to "needs action"; bob receives a new request. |
| **I6** | Alice changes only the title/notes. | Bob's copy updates; bob's earlier answer is acceptable to keep or reset (record which happens). |
| **I7** | Alice cancels the event (deletes it, answering "notify" if asked). | Bob's copy is **cancelled** (struck through or removed). Server: bob's inbox item is now `METHOD:CANCEL`; bob's copy has `STATUS:CANCELLED`. |
| **I8** | Alice invites an address with **no account** (SMTP configured). | An invitation email arrives containing an `.ics` attachment/body with `METHOD:REQUEST`; alice's copy shows `SCHEDULE-STATUS=1.1`. |
| **I9** | Same without SMTP configured. | The event saves normally; `SCHEDULE-STATUS=5.3` for that attendee; the client does not show an error loop. |
| **I10** | Bob deletes an event he was invited to, without answering. | Alice sees bob as **declined**. |
| **I11** | Alice removes bob from the attendee list of an existing event. | Bob's copy is cancelled. |
| **I12** | Invite bob to a **recurring** event; bob accepts one occurrence only (clients ask "this event only?"). | Note the outcome: galdav records replies for existing overrides and the series; a reply for a single plain instance of a series is not (yet) recorded (see [features.md](features.md)). Make sure nothing breaks. |
| **I13** | Use the client's **"Inbox"/"Invitations"** view (Apple) or invitation panel (Thunderbird): accept from there instead of from the event. | Same result as I3. |
| **I14** | Invite bob from a client that can also send invitations by email on its own (Thunderbird, depending on settings). | Bob receives **one** invitation, not two (one from the client and one from galdav). If a client marks attendees `SCHEDULE-AGENT=CLIENT`, galdav leaves delivery to it; check the stored `ATTENDEE` lines to see which happened. |

### F. Free-busy

| ID | Steps | Expect |
|---|---|---|
| **F1** | In alice's client open the invitee availability view (Apple: "Availability" when adding an invitee; Thunderbird: the free/busy timeline in the invitation dialog) and look at bob's day. | Bob's busy blocks appear where bob has events (not details). |
| **F2** | Mark one of bob's events *free*/transparent; decline another. | Those do not show as busy; tentative events show as tentative. |
| **F3** | Look up the free/busy of the external address. | The client shows "unknown" gracefully, not an error. |

### H. Sharing

| ID | Steps | Expect |
|---|---|---|
| **H1** | As alice, share *CT Events* with bob **read-only** (admin UI: collection page → Sharing). Sync bob's client. | Bob's client lists the calendar (named like alice's) and shows its events. |
| **H2** | On bob's client try to add/edit/delete an event in it. | The client **refuses** (read-only calendar) or, if it lets you try, the change fails cleanly with a message and does not stay in the client as if saved. Server: nothing changed. |
| **H3** | Upgrade bob to **can edit**; sync. | Bob can now create/edit/delete; alice sees the changes. |
| **H4** | Bob renames the shared calendar and changes its colour **on his client**. | Only bob's view changes; alice's name/colour are unchanged. |
| **H5** | Bob removes (unsubscribes from) the shared calendar on his client. | It leaves bob's account; alice's calendar and events are untouched. |
| **H6** | Alice stops sharing. | The calendar disappears from bob's client after sync; no error loop, no orphaned local copy. |
| **H7** | Share an **address book** read-only with bob. | Contacts visible to bob; edits refused. |
| **H8** | (Apple only) Share from alice's iOS/macOS client via the sharing UI instead of the admin UI. | Either works (bob gets the calendar immediately) or fails clearly; record exactly what the client says. This exercises the `CS:share` protocol, which is the least certain part of galdav. |

| **H9** | Set `sharing.require_accept: true`, restart, and share *CT Events* with bob from alice's admin UI. Look at bob's client, then at bob's admin start page. | The calendar is **not** in bob's client yet; bob's start page lists an invitation. Accept it: the calendar appears after the next sync. Decline a second one: it never appears. |
| **H10** | (Apple only) With `require_accept` on, share from alice's iOS/macOS client. | Bob's Calendar app shows an invitation (it reads `CS:notification-URL`); accepting adds the calendar, declining does not, and alice's client shows the answer. Record exactly what each side shows; this is the least certain part of the sharing protocol. |
| **H11** | In the admin UI turn on a **public link** for *CT Events*, subscribe to it from a calendar app that supports internet calendar subscriptions (an `https://…/public/<token>.ics` URL, no login). Then press *New address*. | Events show up read-only. After *New address* the old subscription fails cleanly (404) and the new one works. Turning the link off ends access. |

### A. Accounts and authentication

| ID | Steps | Expect |
|---|---|---|
| **A1** | Change alice's password in the admin UI. | Her clients prompt for the new password and resume syncing after entering it; none keep failing silently. |
| **A2** | Reset bob's password as administrator. | Same as A1 for bob. |
| **A3** | Check that the client authenticates with **Basic** over TLS (traffic capture). | Works; no `Digest` challenge is offered. |
| **A4** | Sign in to the admin UI at the same time as clients are syncing. | Independent; neither disturbs the other. |
| **A6** | In the admin UI create an **app password** for the client; sign the client in with it instead of the account password. Then revoke it. | Syncing works with the app password; after revoking, the client asks for credentials again and the account password still works. |
| **A7** | Turn on **two-factor** for alice (admin UI). Check the clients that still use alice's account password. | They are refused (`401`) until given an app password; the admin UI asks for the code. Turning two-factor off restores account-password logins. |
| **A8** | Repeat wrong passwords from one client until it is refused. | After `login_limit.failures` failures the server answers `429`; the failures appear in the log with the client's real address (check `proxy.trusted`). |
| **A5** | Delete bob's account while his client is syncing. | The client reports authentication failure; no crash, no data touched in alice's account. |

### S. Scheduling details

| ID | Steps | Expect |
|---|---|---|
| **S1** | Organizer edits only the **title** of an event that has an external attendee, then moves it to another time. | The title edit sends no new email; the move does. The organizer's client does not report a `412` conflict on either save. |
| **S2** | Organizer's client saves an invitation, then edit it again immediately from the same client (no re-sync). | No `412`; the client picks up the `Schedule-Tag` the server returned. |
| **S3** | Attendee declines **one occurrence** of a weekly series. | The organizer's copy shows that occurrence declined (an override appears), the rest of the series is untouched. |
| **S4** | Ask the client for a month view of a recurring event if it uses `expand`/partial `calendar-data` (traffic capture). | Instances in the right places, across a DST change; nothing missing or doubled. |
| **S5** | (Optional, needs mail piping set up, see the deployment guide.) Answer an invitation from an outside mail client. | `galdav imip-receive` applies the answer; the organizer's copy shows the new status. |

### X. Robustness

| ID | Steps | Expect |
|---|---|---|
| **X1** | Event with a very long description (50 KB) and many attendees (30). | Saves, syncs, displays. |
| **X2** | Event with an attachment (if the client allows it). | Either accepted (attachments are inline or URLs) or refused cleanly; note the size at which it fails (objects are limited to 10 MiB). |
| **X3** | Event far in the past and far in the future (year 1999, year 2100). | No server error or sync failure. |
| **X4** | Calendar and contact names with spaces, unicode and emoji. | Names display correctly everywhere. |
| **X5** | Emoji in event titles and contact names. | Round-trip intact (utf8mb4). |
| **X6** | Rapidly create 20 events on the client. | All arrive once; no duplicates. |

---

## 3. Capturing evidence

A failure report without evidence is a guess. Get at least one of these
**before you change anything else.**

**1. Wire capture between proxy and galdav** (best: full requests and
responses, bodies included, because that hop is plain HTTP on the same host):

```sh
sudo tcpdump -i lo -A -s 0 -w /tmp/dav.pcap 'tcp port 8080'      # then reproduce the problem, Ctrl-C
tcpdump -A -r /tmp/dav.pcap | less                               # or open the file in Wireshark
```

The capture contains passwords (Basic is base64); **redact the
`Authorization` header** before sharing it.

**2. Proxy access log**, for the sequence and status codes (no bodies). With
Caddy:

```
cal.example.com {
	log {
		output file /var/log/caddy/cal.log
		format json
	}
	reverse_proxy 127.0.0.1:8080
}
```

Look at the `request.method`, `request.uri` and `status` fields around the time
of the problem; repeated `401`s mean an authentication problem, `403`/`409`/
`412`/`415` are almost always protocol disagreements worth reporting.

**3. galdav's journal**: `journalctl -u galdav --since '10 minutes ago'`. Only
errors are logged; a `500` always has an `ERROR` line here.

**4. Database state**: the `q` queries from section 1 show what the server
actually stored and the sync log.

**5. Client logs.** DAVx5: *Settings → Logging → Verbose* then *Share log*;
Apple: Console.app filtered on `dataaccessd`, `calaccessd`, `CalendarAgent`,
`AddressBook`; Thunderbird: *Tools → Developer Tools → Error Console*, and
`calendar.debug.log` = true / `calendar.debug.log.verbose` = true in the config
editor.

**What to record with every failure:** scenario ID; client name and version;
OS version; the exact steps; what you expected; what happened (screenshot or
message text); the capture/log excerpt; whether it reproduces.

---

## 4. Record the results

Copy this table into an issue or a notes file and fill it in. Use
✅ pass · ⚠️ works with a workaround or cosmetic problem (describe) ·
❌ fail · — not applicable to this client.

| ID | DAVx5 | iOS/macOS | Thunderbird | Notes / links |
|---|---|---|---|---|
| D1 |  |  |  |  |
| D2 |  |  |  |  |
| D3 |  |  |  |  |
| D4 |  |  |  |  |
| R1 |  |  |  |  |
| R2 |  |  |  |  |
| R3 |  |  |  |  |
| R4 |  |  |  |  |
| R5 |  |  |  |  |
| R6 |  |  |  |  |
| R7 |  |  |  |  |
| R8 |  |  |  |  |
| W1 |  |  |  |  |
| W2 |  |  |  |  |
| W3 |  |  |  |  |
| W4 |  |  |  |  |
| W5 |  |  |  |  |
| W6 |  |  |  |  |
| W7 |  |  |  |  |
| W8 |  |  |  |  |
| C1 |  |  |  |  |
| C2 |  |  |  |  |
| C3 |  |  |  |  |
| C4 |  |  |  |  |
| C5 |  |  |  |  |
| Y1 |  |  |  |  |
| Y2 |  |  |  |  |
| Y3 |  |  |  |  |
| Y4 |  |  |  |  |
| Y5 |  |  |  |  |
| Y6 |  |  |  |  |
| Y7 |  |  |  |  |
| Y8 |  |  |  |  |
| T1 |  |  |  |  |
| T2 |  |  |  |  |
| I1 |  |  |  |  |
| I2 |  |  |  |  |
| I3 |  |  |  |  |
| I4 |  |  |  |  |
| I5 |  |  |  |  |
| I6 |  |  |  |  |
| I7 |  |  |  |  |
| I8 |  |  |  |  |
| I9 |  |  |  |  |
| I10 |  |  |  |  |
| I11 |  |  |  |  |
| I12 |  |  |  |  |
| I13 |  |  |  |  |
| I14 |  |  |  |  |
| F1 |  |  |  |  |
| F2 |  |  |  |  |
| F3 |  |  |  |  |
| H1 |  |  |  |  |
| H2 |  |  |  |  |
| H3 |  |  |  |  |
| H4 |  |  |  |  |
| H5 |  |  |  |  |
| H6 |  |  |  |  |
| H7 |  |  |  |  |
| H8 |  |  |  |  |
| H9 |  |  |  |  |
| H10 |  |  |  |  |
| H11 |  |  |  |  |
| A1 |  |  |  |  |
| A2 |  |  |  |  |
| A3 |  |  |  |  |
| A4 |  |  |  |  |
| A5 |  |  |  |  |
| A6 |  |  |  |  |
| A7 |  |  |  |  |
| A8 |  |  |  |  |
| S1 |  |  |  |  |
| S2 |  |  |  |  |
| S3 |  |  |  |  |
| S4 |  |  |  |  |
| S5 |  |  |  |  |
| X1 |  |  |  |  |
| X2 |  |  |  |  |
| X3 |  |  |  |  |
| X4 |  |  |  |  |
| X5 |  |  |  |  |
| X6 |  |  |  |  |

**Severity**

- **P0 – blocks use**: cannot set up the account, data loss or corruption,
  events at the wrong time, sync that loops or silently stops, a read-only share
  that accepts writes, one user seeing another's data.
- **P1 – major**: a documented feature does not work in a mainstream client
  (invitations not delivered, accept not recorded, free-busy missing, shares not
  shown).
- **P2 – minor**: cosmetic differences, extra round trips, a client quirk with a
  workaround.

**Suggested bar for "ready for daily use"** (per client you intend to support):
no P0; D1–D4, R1–R5, R7, W1–W6, C1–C3, Y1–Y3, A1 all ✅; and, if you use
invitations, I1–I5, I7, I10 ✅ with at least one Apple client and Thunderbird.
Shares (H1–H6) ✅ for DAVx5's read-only handling (H2). Everything else may be
⚠️ if documented in [features.md](features.md).

---

## 5. Client-specific notes

Which scenarios apply, what the client is expected to do (to be **verified**),
and where galdav is most likely to disagree.

### DAVx5 (Android)

Applicable: D, R, W, C, Y, T (with a task app such as tasks.org or OpenTasks
installed), A, X, H1–H7. Not applicable: I-scenarios as an organizer UI differs
by calendar app (use the calendar app you actually use, e.g. Fossify/Google
Calendar, for I-scenarios); F (DAVx5 has no free/busy view).

- Add the account with **Login with URL and user name**, base URL
  `https://cal.example.com/dav/`. DAVx5 walks `current-user-principal`,
  `calendar-home-set` and `addressbook-home-set`, then lists collections and
  asks which to sync (D2: you must tick them).
- It syncs with `sync-collection` and `calendar-multiget`/`addressbook-multiget`;
  if sync is slow or repeats, check the `changes` log (Y1, Y5).
- Read-only handling: DAVx5 decides from `current-user-privilege-set`. **H2 is
  the important test:** a read-only share must be read-only in the calendar app.
- Invitations: DAVx5 does not process the scheduling inbox itself; it relies on
  the server having put the event into the default calendar (I2) and on the
  calendar app. Verify that an invited event appears and that accepting it in
  the calendar app results in the reply reaching the organizer (I3).
- Tasks need a compatible app; check VTODO syncing with the CT Tasks list (R6,
  W7).
- Use *Account → Refresh collection list* after creating or sharing
  collections (H1, H6).

### iOS and macOS (Calendar, Reminders, Contacts)

Applicable: all scenarios. These are the most demanding clients and the ones
most likely to expose property or protocol gaps, especially around scheduling
and sharing.

- Setup: *Settings → Calendar → Accounts → Add Account → Other → Add CalDAV
  Account* (and CardDAV for contacts), server `cal.example.com`, then user and
  password. It should discover everything via `/.well-known/` (D1). If setup
  fails, capture the traffic: **check which path the client starts at**: `/.well-known/…` and `PROPFIND /`
  are answered; anything else (e.g. a Baikal-style `/dav.php`) is not.
- Tasks appear in **Reminders** (the CT Tasks list) and need the calendar
  object type `VTODO` (R6, W7).
- Notice whether Apple adds `If-Schedule-Tag-Match`/`Schedule-Tag` headers when
  editing invitations (galdav honours them; a `412` after an organizer edit is expected, a `412` on
  a plain edit is a finding).
- **Invitations (I2, I13):** Apple shows invitations in the Calendar app's
  *Inbox* and as events needing action; check both, and check that the reply
  arrives (I3).
- **Free/busy (F1):** the "Availability" button in the new-event invitee list
  posts to the outbox; a `schedule-response` problem shows there as an empty or
  "unavailable" timeline.
- **Sharing (H8):** use *Calendars → (i) → Add Person* to share; this is the
  `CS:share` path and is the single least-tested feature. Also see whether a
  calendar shared via the admin UI shows as shared in the (i) panel.
- macOS: Calendar → *Settings → Accounts*; use *File → Refresh* (⌘R) to force
  a sync; Console.app for logs (section 3).

### Thunderbird (Calendar and Address Book)

Applicable: D (via URL), R, W, C (Thunderbird 91+ has CardDAV), Y, I, F, H,
A, X. Tasks are shown in the same calendars (R6, W7).

- Add a calendar: *New Calendar → On the Network → CalDAV*, location
  `https://cal.example.com/dav/calendars/alice/ct-events/`. For address books
  *New Address Book → Add CardDAV Address Book*, with
  `https://cal.example.com/dav/addressbooks/alice/ct-contacts/`.
  "Find Calendars" against `https://cal.example.com/dav/` should list the
  collections (D2).
- Thunderbird decides between server-side and email scheduling from the
  `DAV:` capabilities galdav advertises (`calendar-auto-schedule`). **I14 is the
  important check:** invitations must be sent once, not by both the client (email)
  and the server.
- Thunderbird polls the scheduling inbox and honours `current-user-privilege-set`
  for read-only calendars (H2).
- Free/busy (F1) uses the outbox; open an event, add an attendee and use the
  *Availability* tab.
- If a calendar shows "Unable to access" errors, open the Error Console (section 3)
  before anything else.

### Optional: Evolution, GNOME Calendar, KDE (Kontact/Akonadi)

Run D1–D2, R1–R5, W1, W5, W6, C1 and Y1 only, and add them to the table as
extra columns. They use older or different discovery paths and are useful for
finding strict-parser problems.

---

## 6. Triage map

When something fails, this tells you where in galdav to look first.

| Symptom | Where to look |
|---|---|
| Account cannot be set up / discovery fails | Proxy forwarding of `/.well-known/*`; `internal/dav/server.go` (`serveDAV`, `resolve`/`propfind`), `internal/dav/props.go` (`principalRes`: `calendar-home-set`, `schedule-*-URL`). Check whether the client requested a path galdav returns `404` for. |
| Repeated `401` | `internal/auth` (Basic credentials, realm); wrong password; proxy altering the `Authorization` header or the request URI. |
| `403`/`409`/`412` on PUT | `internal/dav/handlers.go` `put` and `internal/index/index.go` (validation: `METHOD`, UID, component type, `valid-calendar-data`); preconditions on `If-Match`. The error body names the precondition. |
| Items missing/duplicated after sync | `changes` table; `sync-collection` in `internal/dav/report.go`; `PutObject`/`DeleteObject` in `internal/store/objects.go`. |
| Wrong times / recurrences | `internal/index/index.go` (`Span`, `occurrenceBounds`), `internal/dav/calfilter.go` (`overlaps`), `internal/sched/freebusy.go` (`busyInstances`). Compare the stored object (`q`) with what the client sent (capture). |
| Query returns too few/many events | `calendar-query` filters in `internal/dav/calfilter.go`; SQL prefilter in `QueryObjects`. Non-IANA `TZID`s are unindexed but still evaluated. |
| Invitations not delivered / not answered | `internal/sched/service.go` (`ProcessPut`, `deliver`), `internal/sched/apply.go`; the user's **email** (case-insensitive, exact); inbox rows (`itip-…`); `SCHEDULE-STATUS` on the organizer's copy tells the delivery result (`1.2` delivered, `1.1` mailed, `5.3` no way to deliver, `3.7` unknown address). |
| Free-busy empty | `internal/sched/outbox.go` (`HandleOutbox`), `freebusy.go`; calendar marked transparent; events declined or `TRANSP:TRANSPARENT`. |
| Share not visible / writable when it shouldn't be | `internal/store/shares.go`, `internal/dav/props.go` (`privFor`, `sharingProps`), write checks in `handlers.go` (`c.Access`). |
| Apple sharing UI errors | `postCollection` in `handlers.go` and the `CS:` properties in `props.go`. |
| Admin UI problems behind the proxy | `proxy.trusted`, `Host`/`X-Forwarded-Proto` headers, `internal/admin/admin.go` (`clientIP`, `isHTTPS`). |
| `500` errors | the journal line (`journalctl -u galdav`) has the cause. |

A handy way to create many events for Y4 (needs GNU `date`):

```sh
for i in $(seq 1 300); do
  d=$(date -d "2027-01-01 +$i days" +%Y%m%d)
  printf 'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:bulk-%s@t\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:%sT100000Z\r\nDTEND:%sT110000Z\r\nSUMMARY:Bulk %s\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n' "$i" "$d" "$d" "$i" |
  curl -s -o /dev/null -w '%{http_code} ' -u "alice:$PW" -X PUT -H 'Content-Type: text/calendar' --data-binary @- "$BASE/dav/calendars/alice/ct-events/bulk-$i.ics"
done
```

Remove the bulk events afterwards with `seed.sh --clean` (which deletes the
whole test collections) and re-seed.

When the run is finished, put the filled-in table and the list of findings
somewhere durable (an issue per ❌/⚠️), and update
[features.md](features.md) and [roadmap.md](roadmap.md) with what you learned.
