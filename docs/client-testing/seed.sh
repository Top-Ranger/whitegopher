#!/usr/bin/env bash
# Seed a galdav account with the fixtures used by docs/client-testing.md, so
# every client under test sees identical data.
#
#   ./seed.sh https://cal.example.com alice            # prompts for the password
#   CT_PASSWORD=... ./seed.sh https://cal.example.com alice
#   ./seed.sh --clean https://cal.example.com alice    # delete the three test collections
#
# Creates the calendar "ct-events", the task list "ct-tasks" and the address
# book "ct-contacts" (display names start with "CT"), with dates relative to
# today. Needs bash, curl and GNU date (on macOS: brew install coreutils; the
# script uses gdate when present). Safe to re-run: objects are overwritten.
set -euo pipefail

clean=0
if [[ ${1:-} == --clean ]]; then clean=1; shift; fi
[[ $# -eq 2 ]] || { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
base=${1%/}; user=$2
[[ $base == http://* ]] && echo "warning: $base is plain HTTP; the password is sent unencrypted" >&2
if [[ -z ${CT_PASSWORD:-} ]]; then read -rsp "Password for $user: " CT_PASSWORD; echo; fi

here=$(cd "$(dirname "$0")" && pwd)
datecmd=date; command -v gdate >/dev/null && datecmd=gdate
day() { $datecmd -d "+$1 days" +%Y%m%d; }

cal="$base/dav/calendars/$user"
book="$base/dav/addressbooks/$user"
fail=0

req() { # req METHOD URL [curl args...] -> prints status
  local method=$1 url=$2; shift 2
  curl -s -o /dev/null -w '%{http_code}' --basic -u "$user:$CT_PASSWORD" -X "$method" "$@" "$url"
}
check() { # check LABEL ACTUAL EXPECTED...
  local label=$1 got=$2; shift 2
  for want in "$@"; do [[ $got == "$want" ]] && { printf '  %-28s %s\n' "$label" "$got"; return; }; done
  printf '  %-28s %s  <-- FAILED (expected %s)\n' "$label" "$got" "$*"; fail=1
}

if (( clean )); then
  for c in "$cal/ct-events/" "$cal/ct-tasks/" "$book/ct-contacts/"; do
    check "delete ${c##*/dav/}" "$(req DELETE "$c")" 204 404
  done
  exit $fail
fi

mkcal() { # mkcal SLUG NAME COMPONENT COLOR
  req MKCALENDAR "$cal/$1/" -H 'Content-Type: application/xml' --data-binary \
"<c:mkcalendar xmlns:c=\"urn:ietf:params:xml:ns:caldav\" xmlns:d=\"DAV:\" xmlns:i=\"http://apple.com/ns/ical/\"><d:set><d:prop>
<d:displayname>$2</d:displayname><i:calendar-color>$4</i:calendar-color>
<c:supported-calendar-component-set><c:comp name=\"$3\"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>"
}

echo "Collections on $base for $user"
check "calendar ct-events"  "$(mkcal ct-events 'CT Events' VEVENT '#2f6fed')" 201 405
check "task list ct-tasks"  "$(mkcal ct-tasks  'CT Tasks'  VTODO  '#c5302a')" 201 405
check "address book"        "$(req MKCOL "$book/ct-contacts/" -H 'Content-Type: application/xml' --data-binary \
'<mkcol xmlns="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><set><prop><resourcetype><collection/><a:addressbook/></resourcetype><displayname>CT Contacts</displayname></prop></set></mkcol>')" 201 405

put() { # put COLLECTION_URL FILE CONTENT_TYPE
  local name; name=$(basename "$2")
  # Substitute dates, then convert to CRLF line endings as the RFCs require.
  sed -e "s/@D1@/$(day 1)/g;  s/@D2@/$(day 2)/g;  s/@D3@/$(day 3)/g;  s/@D4@/$(day 4)/g" \
      -e "s/@D5@/$(day 5)/g;  s/@D8@/$(day 8)/g;  s/@D15@/$(day 15)/g" -e 's/$/\r/' "$2" |
    curl -s -o /dev/null -w '%{http_code}' --basic -u "$user:$CT_PASSWORD" -X PUT \
      -H "Content-Type: $3" --data-binary @- "$1$name"
}

echo "Events"
for f in allday weekly-berlin newyork floating rich; do
  check "$f.ics" "$(put "$cal/ct-events/" "$here/fixtures/$f.ics" 'text/calendar; charset=utf-8')" 201 204
done
echo "Tasks"
for f in todo todo-undated; do
  check "$f.ics" "$(put "$cal/ct-tasks/" "$here/fixtures/$f.ics" 'text/calendar; charset=utf-8')" 201 204
done
echo "Contacts"
for f in contact-basic contact-unicode contact-v4 contact-photo; do
  check "$f.vcf" "$(put "$book/ct-contacts/" "$here/fixtures/$f.vcf" 'text/vcard; charset=utf-8')" 201 204
done

if (( fail )); then echo "Seeding had failures."; exit 1; fi
echo "Done. Dates used: tomorrow = $(day 1), weekly series starts $(day 1)."
