# Mail Filter Rules

This document outlines the rules and options available for creating email filters in `filters.yml`.

## Filter Structure

Each filter is a YAML object with the following properties:

- `name` (required): A descriptive name for the filter.
- `mailboxes` (optional): A list of mailboxes to apply the filter to. If omitted, the filter will only apply to the `INBOX`.
- `conditions_type` (optional): Specifies how the conditions are evaluated. Can be `and` (all conditions must match) or `or` (any condition can match). Defaults to `and`.
- `conditions` (required): A list of conditions that a message must meet for the actions to be triggered.
- `actions` (required): A list of actions to perform on a message that matches the conditions.
- `disabled` (optional): Set to `true` to keep the filter in the file without running it.
- `active_from` (optional): RFC 3339 timestamp (for example `2026-10-01T08:00:00Z`). The filter does not run before this time. Once the time passes, the field is removed from the file and the filter runs like any other.
- `active_until` (optional): RFC 3339 timestamp. The filter stops running at this time. Once the time passes, the filter is either disabled or removed, depending on `on_expire`.
- `on_expire` (optional): What happens when `active_until` is reached: `disable` (default) sets `disabled: true` and clears the schedule; `remove` deletes the filter from the file.

Schedule boundaries are checked once a minute. Every change is written back to `filters.yml`, which triggers the usual full filter run, so a filter that activates on schedule is applied to existing mail as well.

---

## Conditions

Conditions define the criteria for matching an email.

### Fields

You can filter on the following email fields:

- `from`: The sender's email address.
- `sender`: The sender's full name and email address.
- `to`: The recipient's email address.
- `cc`: The Cc'd recipient's email address.
- `bcc`: The Bcc'd recipient's email address.
- `from/to`: Checks both the sender's and recipient's full name and email address.
- `from/to/cc/bcc`: Checks the From, To, Cc, and Bcc fields, including full names and email addresses.
- `subject`: The subject line of the email.
- `body`: The body content of the email.
- `age`: The age of the email.
- `status`: The read/unread status of the email.

### Operators

You can use the following operators for comparison:

- `equals`: The field must exactly match the `value` (case-insensitive).
- `contains`: The field must contain the `value` (case-insensitive).
- `not-contains`: The field must not contain the `value` (case-insensitive).
- `begins_with`: The field must begin with the `value` (case-insensitive).
- `ends_with`: The field must end with the `value` (case-insensitive).
- `older_than`: Used with the `age` field. The value is a duration string (e.g., "30d" for 30 days, "2w" for 2 weeks, "1m" for 1 month, "1y" for 1 year).

**Note:** The `status` field does not use an operator. The `value` can be either `read` or `unread`.

---

## Actions

Actions define what to do with a matched email.

- `type: "move"`: Moves the email to another mailbox.
  - `value` (required): The destination mailbox (e.g., `"INBOX.Newsletters"` or `"subs.Github"`).
- `type: "mark_as_read"`: Marks the email as read.
- `type: "flag"`: Adds a flag to the email.
  - `value` (required): The name of the flag to add (e.g., `"Important"` or `"Starred"`).
- `type: "delete"`: Deletes the email.

---

## Examples

Here are some examples of complete filters.

### Example 1: Move Newsletters to a Subfolder

This filter moves any email from an address containing "newsletter" into the `INBOX.Newsletters` folder.

```yaml
- name: "Sort Newsletters"
  conditions:
    - field: "from"
      operator: "contains"
      value: "newsletter"
  actions:
    - type: "move"
      value: "INBOX.Newsletters"
```

### Example 2: Flag Emails from a Specific Sender

This filter marks emails from "boss@example.com" as read and adds the "Important" flag. It only applies to the `INBOX`.

```yaml
- name: "Important from Boss"
  conditions:
    - field: "from"
      operator: "equals"
      value: "boss@example.com"
  actions:
    - type: "mark_as_read"
    - type: "flag"
      value: "Important"
```

### Example 3: Move Spam and Apply to Multiple Mailboxes

This filter moves emails with "[SPAM]" in the subject to the `Junk` folder. It runs on both the `INBOX` and `subs` mailboxes.

```yaml
- name: "Junk Mail"
  mailboxes:
    - "INBOX"
    - "subs"
  conditions:
    - field: "subject"
      operator: "contains"
      value: "[SPAM]"
  actions:
    - type: "move"
      value: "Junk"

### Example 4: Filter by Recipient

This filter moves any email sent to "project-team@example.com" to the `Projects.Team` folder.

```yaml
- name: "Project Team Emails"
  conditions:
    - field: "to"
      operator: "equals"
      value: "project-team@example.com"
  actions:
    - type: "move"
      value: "Projects.Team"
```

### Example 5: Filter by Sender or Recipient

This filter moves any email that is either from or to "jane.doe@example.com" to the `People.Jane` folder.

```yaml
- name: "Jane Doe Correspondence"
  conditions:
    - field: "from/to"
      operator: "equals"
      value: "jane.doe@example.com"
  actions:
    - type: "move"
      value: "People.Jane"
```

### Example 6: Delete Old Emails

This filter deletes any email in the `subs.gitlab.backport-bot` mailbox that is older than 30 days.

```yaml
- name: "Delete old backport bot emails"
  mailboxes:
    - "subs.gitlab.backport-bot"
  conditions:
    - field: "age"
      operator: "older_than"
      value: "30d"
  actions:
    - type: "delete"
```

### Example 7: Filter by From, To, Cc, or Bcc

This filter moves any email where "management@example.com" is in the From, To, Cc, or Bcc field to the `Archive.Management.Example` folder.

```yaml
- name: "Management Correspondence (Example)"
  conditions:
    - field: "from/to/cc/bcc"
      operator: "contains"
      value: "management@example.com"
  actions:
    - type: "move"
      value: "Archive.Management.Example"
```

### Example 8: Filter by Body Content

This filter moves any email in the `subs.gitlab` mailbox that contains "created an issue" in the body to the `subs.gitlab.issues` folder.

```yaml
- name: "GitLab Issues"
  mailboxes:
    - "subs.gitlab"
  conditions:
    - field: "body"
      operator: "contains"
      value: "created an issue"
  actions:
    - type: "move"
      value: "subs.gitlab.issues"
```

### Example 9: Filter by Exclusion

This filter moves any email in the `INBOX` that does not contain "urgent" in the subject to the `Archive.Example` folder.

```yaml
- name: "Archive Non-Urgent (Example)"
  mailboxes:
    - "INBOX"
  conditions:
    - field: "subject"
      operator: "not-contains"
      value: "urgent"
  actions:
    - type: "move"
      value: "Archive.Example"
```

### Example 10: Flag Unread Mail

This filter adds the "Follow Up" flag to all unread emails in the `INBOX`.

```yaml
- name: "Flag Unread Mail"
  mailboxes:
    - "INBOX"
  conditions:
    - field: "status"
      value: "unread"
  actions:
    - type: "flag"
      value: "Follow Up"
```

### Example 11: Promotions Filter with "or"

This filter moves any email with "sale" or "offer" in the subject to the `Promotions` folder. It uses `conditions_type: "or"` to match if *any* of the conditions are true.

```yaml
- name: "Promotions"
  mailboxes:
    - "example"
  conditions_type: "or"
  conditions:
    - field: "subject"
      operator: "contains"
      value: "sale"
    - field: "subject"
      operator: "contains"
      value: "offer"
  actions:
    - type: "move"
      value: "Promotions"
    - type: "mark_as_read"
```
