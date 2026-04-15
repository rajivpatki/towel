GMAIL_TOOL_DEFINITIONS = [
    {
        "name": "gmail.list_labels",
        "gmail_actions": ["users.labels.list"],
        "description": "Loads the user\'s current Gmail labels so Towel can learn the existing taxonomy and avoid mixing with non-Towel labels.",
        "safety_model": "read_only",
    },
    {
        "name": "gmail.create_towel_label",
        "gmail_actions": ["users.labels.create"],
        "description": "Creates labels strictly under the Towel/ hierarchy, such as Towel/Spam, Towel/Delete, or learned organizational labels.",
        "safety_model": "safe_write",
    },
    {
        "name": "gmail.list_messages",
        "gmail_actions": ["users.messages.list"],
        "description": "Finds unread, high-volume, or suspicious message sets for analysis and triage.",
        "safety_model": "read_only",
    },
    {
        "name": "gmail.read_message",
        "gmail_actions": ["users.messages.get"],
        "description": "Reads individual email content and metadata so the agent can classify, summarize, and detect scams or spam patterns.",
        "safety_model": "read_only",
    },
    {
        "name": "gmail.move_to_towel_spam",
        "gmail_actions": ["users.messages.batchModify", "users.labels.create"],
        "description": "Applies or creates Towel/Spam and removes inbox visibility instead of using Gmail\'s destructive spam action.",
        "safety_model": "pseudo_spam",
    },
    {
        "name": "gmail.move_to_towel_delete",
        "gmail_actions": ["users.messages.batchModify", "users.labels.create"],
        "description": "Applies or creates Towel/Delete and removes inbox visibility instead of deleting messages.",
        "safety_model": "pseudo_delete",
    },
    {
        "name": "gmail.create_filter",
        "gmail_actions": ["users.settings.filters.create", "users.labels.create"],
        "description": "Creates Gmail filters that route future emails into existing or new Towel/ labels based on learned rules and user preferences.",
        "safety_model": "safe_write",
    },
    {
        "name": "gmail.sender_analytics",
        "gmail_actions": ["users.messages.list", "users.messages.get"],
        "description": "Aggregates sender and domain volume statistics so the UI can recommend cleanup or automation actions.",
        "safety_model": "read_only",
    },
]


def list_gmail_tools() -> list[dict]:
    return GMAIL_TOOL_DEFINITIONS
