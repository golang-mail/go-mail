# Filter Configuration

This document describes the structure and options for defining filters in the `filters.yml` file.

## Filter Structure

Each filter is a YAML object with the following fields:

-   `name`: A descriptive name for the filter.
-   `accounts`: (Optional) A list of account names to which this filter should apply. If this field is omitted, the filter will apply to all accounts.
-   `mailboxes`: (Optional) A list of mailboxes to which this filter should apply. If omitted, the filter will only apply to the `INBOX`.
-   `conditions_type`: (Optional) The logical operator to use when combining multiple conditions. Can be `and` (default) or `or`.
-   `conditions`: A list of conditions that an email must meet for the filter to match.
-   `actions`: A list of actions to perform on a matched email.

## Example

```yaml
filters:
  - name: "Important from Boss"
    accounts: ["Zlatko Bratkovic"]
    mailboxes:
      - "INBOX"
    conditions:
      - field: "from"
        operator: "equals"
        value: "boss@example.com"
    actions:
      - type: "mark_as_read"
      - type: "flag"
        value: "Important"
