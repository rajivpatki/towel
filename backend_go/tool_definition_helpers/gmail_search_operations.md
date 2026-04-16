## Query paramenters for search and filter

Here are some search operation query parameters and their possible values with examples that can be used to refine searches in relation to the user's query. Use them creatively to search and filter email instead of brute forcing expensive and slow list operations.

These query parameters can be combined in any way to create complex searches. You can also use the query terms to define automated filters for GMails actions like labelling, archiving, and deleting.

### Logical operators

```json
{
  "from": {
    "description": "Find emails sent from a specific sender.",
    "syntax": "from:<sender>",
    "options": {
      "<email_address>": "Match messages from the specified email address.",
      "<name_or_text>": "Match messages from a sender matching the entered text.",
      "me": "Match messages sent by the authenticated Gmail user."
    },
    "examples": [
      "from:amy@example.com",
      "from:me"
    ]
  },
  "to": {
    "description": "Find emails sent to a specific recipient.",
    "syntax": "to:<recipient>",
    "options": {
      "<email_address>": "Match messages addressed to the specified email address.",
      "<name_or_text>": "Match messages addressed to a recipient matching the entered text.",
      "me": "Match messages addressed to the authenticated Gmail user."
    },
    "examples": [
      "to:john@example.com",
      "to:me"
    ]
  },
  "cc": {
    "description": "Find emails with a specific person in the Cc field.",
    "syntax": "cc:<recipient>",
    "options": {
      "<email_address>": "Match messages where the specified address appears in Cc.",
      "<name_or_text>": "Match messages where the entered text matches a Cc recipient."
    },
    "examples": [
      "cc:john@example.com"
    ]
  },
  "bcc": {
    "description": "Find emails with a specific person in the Bcc field.",
    "syntax": "bcc:<recipient>",
    "options": {
      "<email_address>": "Match messages where the specified address appears in Bcc.",
      "<name_or_text>": "Match messages where the entered text matches a Bcc recipient."
    },
    "examples": [
      "bcc:david@example.com"
    ]
  },
  "subject": {
    "description": "Find emails by words or phrases in the subject line.",
    "syntax": "subject:<text>",
    "options": {
      "<word>": "Match a word in the subject.",
      "<phrase>": "Match a phrase in the subject. Use quotes for exact phrase matching if needed."
    },
    "examples": [
      "subject:dinner",
      "subject:\"anniversary party\""
    ]
  },
  "after": {
    "description": "Find emails after a date or timestamp. In the Gmail API, date literals are interpreted as midnight PST unless you use epoch seconds.",
    "syntax": "after:<date_or_epoch_seconds>",
    "options": {
      "YYYY/MM/DD": "Date format documented by Google, for example 2004/04/16.",
      "MM/DD/YYYY_or_locale_variant": "Google's Help examples also show locale-style date input in the UI help pages.",
      "<epoch_seconds>": "For precise timezone handling in the Gmail API."
    },
    "examples": [
      "after:2004/04/16",
      "after:1388552400"
    ]
  },
  "before": {
    "description": "Find emails before a date or timestamp. In the Gmail API, date literals are interpreted as midnight PST unless you use epoch seconds.",
    "syntax": "before:<date_or_epoch_seconds>",
    "options": {
      "YYYY/MM/DD": "Date format documented by Google, for example 2004/04/18.",
      "MM/DD/YYYY_or_locale_variant": "Google's Help examples also show locale-style date input in the UI help pages.",
      "<epoch_seconds>": "For precise timezone handling in the Gmail API."
    },
    "examples": [
      "before:2004/04/18",
      "before:1391230800"
    ]
  },
  "older": {
    "description": "Find emails older than a given date/time boundary. Google groups this with received-during-a-certain-time-period operators but does not separately enumerate additional fixed values.",
    "syntax": "older:<date_or_time_reference>",
    "options": {
      "<date_or_time_reference>": "Use a date/time reference to search older messages."
    },
    "examples": [
      "older:2004/04/18"
    ]
  },
  "newer": {
    "description": "Find emails newer than a given date/time boundary. Google groups this with received-during-a-certain-time-period operators but does not separately enumerate additional fixed values.",
    "syntax": "newer:<date_or_time_reference>",
    "options": {
      "<date_or_time_reference>": "Use a date/time reference to search newer messages."
    },
    "examples": [
      "newer:2004/04/16"
    ]
  },
  "older_than": {
    "description": "Find emails older than a relative time period.",
    "syntax": "older_than:<number><unit>",
    "options": {
      "<number>d": "Older than the specified number of days.",
      "<number>m": "Older than the specified number of months.",
      "<number>y": "Older than the specified number of years."
    },
    "examples": [
      "older_than:1y",
      "older_than:30d"
    ]
  },
  "newer_than": {
    "description": "Find emails newer than a relative time period.",
    "syntax": "newer_than:<number><unit>",
    "options": {
      "<number>d": "Newer than the specified number of days.",
      "<number>m": "Newer than the specified number of months.",
      "<number>y": "Newer than the specified number of years."
    },
    "examples": [
      "newer_than:2d",
      "newer_than:3m"
    ]
  },
  "OR": {
    "description": "Logical OR. Match emails satisfying one or more search criteria.",
    "syntax": "<expr1> OR <expr2>",
    "options": {
      "OR": "Boolean OR between search clauses."
    },
    "examples": [
      "from:amy OR from:david"
    ]
  },
  "{}": {
    "description": "Alternative OR grouping syntax. Equivalent to an OR across grouped terms.",
    "syntax": "{<expr1> <expr2> ...}",
    "options": {
      "{...}": "Return messages matching any of the enclosed expressions."
    },
    "examples": [
      "{from:amy from:david}"
    ]
  },
  "AND": {
    "description": "Logical AND. Match emails satisfying all criteria.",
    "syntax": "<expr1> AND <expr2>",
    "options": {
      "AND": "Boolean AND between search clauses."
    },
    "examples": [
      "from:amy AND to:david"
    ]
  },
  "-": {
    "description": "Negation. Exclude emails matching the following term or operator. Gmail may still surface a conversation if another message in the same conversation matches.",
    "syntax": "-<term_or_operator>",
    "options": {
      "-<word>": "Exclude messages containing a word.",
      "-<operator:value>": "Exclude messages matching an operator clause."
    },
    "examples": [
      "dinner -movie",
      "-is:starred"
    ]
  },
  "AROUND": {
    "description": "Proximity search. Find words near each other. Add quotes if the first word must stay first.",
    "syntax": "<term1> AROUND <number> <term2>",
    "options": {
      "<number>": "Maximum number of words apart."
    },
    "examples": [
      "holiday AROUND 10 vacation",
      "\"secret AROUND 25 birthday\""
    ]
  },
  "label": {
    "description": "Find emails under a label.",
    "syntax": "label:<label_name>",
    "options": {
      "<user_label_name>": "Match messages with the specified user label.",
      "important": "Google explicitly shows label:important as an example.",
      "encryptedmail": "Google explicitly documents label:encryptedmail for client-side encrypted mail."
    },
    "examples": [
      "label:friends",
      "label:important",
      "label:encryptedmail"
    ]
  },
  "category": {
    "description": "Find emails in Gmail inbox categories.",
    "syntax": "category:<category_name>",
    "options": {
      "primary": "Primary category.",
      "social": "Social category.",
      "promotions": "Promotions category.",
      "updates": "Updates category.",
      "forums": "Forums category.",
      "reservations": "Reservations category.",
      "purchases": "Purchases category."
    },
    "examples": [
      "category:primary",
      "category:promotions"
    ]
  },
  "has": {
    "description": "Check for the existence of specific content types, star/marker types, or label state.",
    "syntax": "has:<option>",
    "options": {
      "attachment": "Message has an attachment.",
      "youtube": "Message includes a YouTube video.",
      "drive": "Message includes a Google Drive file.",
      "document": "Message includes a Google Docs file.",
      "spreadsheet": "Message includes a Google Sheets file.",
      "presentation": "Message includes a Google Slides file.",
      "yellow-star": "Message has the yellow star marker.",
      "orange-star": "Message has the orange star marker.",
      "red-star": "Message has the red star marker.",
      "purple-star": "Message has the purple star marker.",
      "blue-star": "Message has the blue star marker.",
      "green-star": "Message has the green star marker.",
      "red-bang": "Message has the red exclamation marker.",
      "orange-guillemet": "Message has the orange guillemet marker.",
      "yellow-bang": "Message has the yellow exclamation marker.",
      "green-check": "Message has the green check/tick marker.",
      "blue-info": "Message has the blue info marker.",
      "purple-question": "Message has the purple question marker.",
      "userlabels": "Message has one or more labels on the message.",
      "nouserlabels": "Message has no labels on the message.",
      "<star_option_via_locale_variant>": "Some Help Center locale pages render red-bang as red-exclamation mark, yellow-bang as yellow-exclamation mark, and green-check as green-tick. These are localization/rendering variants of the same documented marker set."
    },
    "examples": [
      "has:attachment",
      "has:drive",
      "has:yellow-star",
      "has:userlabels",
      "has:nouserlabels"
    ]
  },
  "list": {
    "description": "Find emails from a mailing list.",
    "syntax": "list:<mailing_list_address>",
    "options": {
      "<email_address>": "Match messages from the specified mailing list."
    },
    "examples": [
      "list:info@example.com"
    ]
  },
  "filename": {
    "description": "Find emails with attachments matching a file name or file type.",
    "syntax": "filename:<name_or_extension>",
    "options": {
      "<extension>": "Match by attachment extension, for example pdf.",
      "<exact_filename>": "Match by attachment filename, for example homework.txt."
    },
    "examples": [
      "filename:pdf",
      "filename:homework.txt"
    ]
  },
  "\"\"": {
    "description": "Exact word or exact phrase match.",
    "syntax": "\"<exact text>\"",
    "options": {
      "\"<phrase>\"": "Match the exact phrase."
    },
    "examples": [
      "\"dinner and movie tonight\""
    ]
  },
  "()": {
    "description": "Group multiple search terms together.",
    "syntax": "(<expr1> <expr2> ...)",
    "options": {
      "(...)": "Group terms to control how expressions combine."
    },
    "examples": [
      "subject:(dinner movie)"
    ]
  },
  "in": {
    "description": "Search within a broad Gmail location/state that Google explicitly documents on the Help page.",
    "syntax": "in:<location>",
    "options": {
      "anywhere": "Search across Gmail, including Spam and Trash.",
      "archive": "Search archived messages.",
      "snoozed": "Search snoozed messages."
    },
    "examples": [
      "in:anywhere movie",
      "in:archive payment reminder",
      "in:snoozed birthday reminder"
    ]
  },
  "is": {
    "description": "Search by message status that Google explicitly documents.",
    "syntax": "is:<status>",
    "options": {
      "important": "Message is marked important.",
      "starred": "Message is starred.",
      "unread": "Message is unread.",
      "read": "Message is read.",
      "muted": "Message is muted."
    },
    "examples": [
      "is:important",
      "is:starred",
      "is:unread",
      "is:read",
      "is:muted"
    ]
  },
  "deliveredto": {
    "description": "Find emails delivered to a specific address.",
    "syntax": "deliveredto:<email_address>",
    "options": {
      "<email_address>": "Match messages delivered to the specified address."
    },
    "examples": [
      "deliveredto:username@example.com"
    ]
  },
  "size": {
    "description": "Find emails by exact size threshold expression as supported by Gmail search.",
    "syntax": "size:<bytes>",
    "options": {
      "<integer_bytes>": "Message size in bytes, for example 1000000."
    },
    "examples": [
      "size:1000000"
    ]
  },
  "larger": {
    "description": "Find emails larger than a specified size. Size often grows because of attachments or inline content.",
    "syntax": "larger:<size>",
    "options": {
      "<integer_bytes>": "Specify bytes directly.",
      "<number>M": "Specify megabytes, for example 10M."
    },
    "examples": [
      "larger:10M",
      "larger:1000000"
    ]
  },
  "smaller": {
    "description": "Find emails smaller than a specified size.",
    "syntax": "smaller:<size>",
    "options": {
      "<integer_bytes>": "Specify bytes directly.",
      "<number>M": "Specify megabytes."
    },
    "examples": [
      "smaller:500000",
      "smaller:2M"
    ]
  },
  "+": {
    "description": "Match a word exactly.",
    "syntax": "+<word>",
    "options": {
      "<word>": "Require the exact word."
    },
    "examples": [
      "+unicorn"
    ]
  },
  "rfc822msgid": {
    "description": "Find emails by the Message-ID header.",
    "syntax": "rfc822msgid:<message_id>",
    "options": {
      "<message_id_header_value>": "Match the exact RFC 822 Message-ID header value."
    },
    "examples": [
      "rfc822msgid:200503292@example.com"
    ]
  }
}
```


### Examples:

1. When a logical operator is not specified between query terms, Gmail uses AND by default.
> User: Find emails from amazon that are not tagged to any label
> Assistant: from:amazon in:inbox -has:userlabels

2. Use parentheses to group terms and create more complex searches.
> User: Find emails from amazon or ebay that are not tagged to any label
> Assistant: (from:amazon OR from:ebay) in:inbox -has:userlabels

3. Use the "has" operator to search for emails with specific characteristics.
> User: How can I clean up emails that are consuming my google storage space
> Assistant: has:attachment larger:5M in:inbox -has:userlabels

### Tips

- If a user asks you to create a filter that performs an automatic action on future emails, such as archiving, marking as unread or tagging a label, evaluate if, or ask the user if they would like to apply the filter to past emails as well. You can use `batchModify` tool to apply the filter to past emails.