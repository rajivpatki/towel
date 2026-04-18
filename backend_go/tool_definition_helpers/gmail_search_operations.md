## Gmail search query parameters

Use this Gmail query tool to narrow email searches and build filters. Prefer the SQL query tool when synced state already contains the needed messages and columns, because SQL is more flexible and cheaper than broad Gmail list calls. Use Gmail search when messages may be outside the sync window or when Gmail-only metadata is required.

### Core rules

- Terms can be combined freely.
- Omitted logical operators default to `AND`.
- Use `()` to group clauses.
- Use `OR` or `{...}` for alternatives.
- Use `-` to exclude a term or operator.
- Use quotes for exact phrases.
- For Gmail API date precision, prefer epoch seconds with `after:` and `before:` because date literals are interpreted as midnight PST.

### Addressing and identity

- `from:<sender>`: sender email, name text, or `me`
- `to:<recipient>`: recipient email, name text, or `me`
- `cc:<recipient>`: Cc recipient
- `bcc:<recipient>`: Bcc recipient
- `deliveredto:<email>`: delivered-to address
- `list:<email>`: mailing-list address
- `rfc822msgid:<message-id>`: exact Message-ID header

Examples:

```text
from:amy@example.com
to:me
cc:john@example.com
list:info@example.com
```

### Content and text matching

- `subject:<text>`: subject contains word or phrase
- `"exact phrase"`: exact phrase match
- `+word`: exact word match
- `term1 AROUND n term2`: proximity search

Examples:

```text
subject:"anniversary party"
"dinner and movie tonight"
+unicorn
holiday AROUND 10 vacation
```

### Time filters

- `after:<date|epoch>`
- `before:<date|epoch>`
- `older:<date_or_time_reference>`
- `newer:<date_or_time_reference>`
- `older_than:<n><d|m|y>`
- `newer_than:<n><d|m|y>`

Examples:

```text
after:2004/04/16
before:1391230800
older_than:30d
newer_than:3m
```

### State, labels, and location

- `label:<name>`: user label or documented labels such as `important`, `encryptedmail`
- `category:<primary|social|promotions|updates|forums|reservations|purchases>`
- `in:<anywhere|archive|snoozed>`
- `is:<important|starred|unread|read|muted>`
- `has:userlabels`: has one or more labels
- `has:nouserlabels`: has no labels

Examples:

```text
label:friends
category:promotions
in:archive
is:unread
-has:userlabels
```

### Attachments, files, and size

- `has:attachment`
- `has:youtube|drive|document|spreadsheet|presentation`
- `filename:<extension_or_name>`
- `size:<bytes>`
- `larger:<bytes|nM>`
- `smaller:<bytes|nM>`

Examples:

```text
has:attachment
has:drive
filename:pdf
larger:10M
smaller:500000
```

### Stars and markers

`has:` also supports Gmail marker variants:

- Stars: `yellow-star`, `orange-star`, `red-star`, `purple-star`, `blue-star`, `green-star`
- Other markers: `red-bang`, `orange-guillemet`, `yellow-bang`, `green-check`, `blue-info`, `purple-question`

Some help pages localize a few names, such as `red-exclamation mark` for `red-bang` and `green-tick` for `green-check`.

### Logical patterns

- `a b`: implicit `AND`
- `a AND b`: explicit `AND`
- `a OR b`: explicit `OR`
- `{a b c}`: grouped `OR`
- `(a b)`: grouped clause
- `-a`: negation

Examples:

```text
from:amy AND to:david
from:amy OR from:david
{from:amy from:david}
(from:amazon OR from:ebay) -has:userlabels
```

### Example queries

1. Find inbox emails from Amazon without labels:

```text
from:amazon in:inbox -has:userlabels
```

2. Find inbox emails from Amazon or eBay without labels:

```text
(from:amazon OR from:ebay) in:inbox -has:userlabels
```

3. Find large unlabeled emails with attachments:

```text
has:attachment larger:5M in:inbox -has:userlabels
```

### Filter tip

If the user asks for a filter that affects future mail, also decide whether past matching messages should be updated. If needed, use `batchModify` to apply the same action to existing messages.
