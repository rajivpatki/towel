from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from api.llm import build_async_client, get_agent_definition
from api.models import Preference, SecretRecord, SetupState
from api.security import decrypt_secret


async def process_chat_message(session: AsyncSession, user_message: str) -> dict:
    state_result = await session.execute(select(SetupState).where(SetupState.id == 1))
    state = state_result.scalar_one_or_none()

    if not state or not state.selected_agent_id:
        raise ValueError("LLM not configured. Please complete setup first.")

    secret_result = await session.execute(
        select(SecretRecord).where(SecretRecord.key == "llm_api_key")
    )
    secret = secret_result.scalar_one_or_none()

    if not secret:
        raise ValueError("LLM API key not found. Please complete setup first.")

    api_key = decrypt_secret(secret.value)
    agent_def = get_agent_definition(state.selected_agent_id)
    llm = build_async_client(state.selected_agent_id, api_key)

    preferences = await session.execute(select(Preference))
    user_preferences = preferences.scalars().all()

    system_prompt = "You are Towel, an AI assistant for Gmail organization. You help users manage their emails safely."
    if user_preferences:
        prefs_text = "\n".join([f"- {p.content}" for p in user_preferences])
        system_prompt += f"\n\nUser preferences:\n{prefs_text}"

    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_message}
    ]

    try:
        response = await llm.chat.completions.create(
            model=agent_def["model"],
            messages=messages,
            temperature=0.7,
            max_tokens=1000
        )

        assistant_message = response.choices[0].message.content

        return {
            "response": assistant_message,
            "actions": []
        }
    except Exception as e:
        raise ValueError(f"Chat processing failed: {str(e)}")
