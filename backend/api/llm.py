from openai import AsyncOpenAI

AGENT_DEFINITIONS = [
    {
        "agent_id": "openai:gpt-5.4",
        "provider": "openai",
        "label": "OpenAI GPT 5.4",
        "model": "gpt-5.4",
        "reasoning_mode": "thinking",
        "verbosity": "low",
        "base_url": None,
    },
    {
        "agent_id": "deepseek:deepseek-thinking",
        "provider": "deepseek",
        "label": "DeepSeek Thinking",
        "model": "deepseek-reasoner",
        "reasoning_mode": "thinking",
        "verbosity": "low",
        "base_url": "https://api.deepseek.com/v1",
    },
]


def list_agents() -> list[dict]:
    return AGENT_DEFINITIONS


def get_agent_definition(agent_id: str) -> dict:
    for agent in AGENT_DEFINITIONS:
        if agent["agent_id"] == agent_id:
            return agent
    raise ValueError(f"Unsupported agent: {agent_id}")


def build_async_client(agent_id: str, api_key: str) -> AsyncOpenAI:
    agent = get_agent_definition(agent_id)
    client_config = {"api_key": api_key}
    if agent["base_url"]:
        client_config["base_url"] = agent["base_url"]
    return AsyncOpenAI(**client_config)
