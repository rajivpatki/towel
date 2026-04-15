import base64
import hashlib
import json
import secrets
from urllib.parse import urlencode

import httpx
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from api.config import get_settings
from api.gmail_tools import list_gmail_tools
from api.llm import get_agent_definition, list_agents
from api.models import SecretRecord, SetupState
from api.schemas import SetupStatus
from api.security import decrypt_secret, encrypt_secret

settings = get_settings()

GOOGLE_AUTH_ENDPOINT = "https://accounts.google.com/o/oauth2/v2/auth"
GOOGLE_TOKEN_ENDPOINT = "https://oauth2.googleapis.com/token"
GOOGLE_USERINFO_ENDPOINT = "https://openidconnect.googleapis.com/v1/userinfo"
GOOGLE_SCOPES = [
    "openid",
    "email",
    "https://www.googleapis.com/auth/gmail.modify",
    "https://www.googleapis.com/auth/gmail.labels",
    "https://www.googleapis.com/auth/gmail.settings.basic",
]


async def get_setup_state(session: AsyncSession) -> SetupState:
    result = await session.execute(select(SetupState).where(SetupState.id == 1))
    state = result.scalar_one_or_none()
    if state is None:
        state = SetupState(id=1)
        session.add(state)
        await session.commit()
        await session.refresh(state)
    return state


async def _get_secret_record(session: AsyncSession, key: str) -> SecretRecord | None:
    result = await session.execute(select(SecretRecord).where(SecretRecord.key == key))
    return result.scalar_one_or_none()


async def has_secret(session: AsyncSession, key: str) -> bool:
    return await _get_secret_record(session, key) is not None


async def get_secret(session: AsyncSession, key: str) -> str | None:
    record = await _get_secret_record(session, key)
    if record is None:
        return None
    return decrypt_secret(record.value)


async def upsert_secret(session: AsyncSession, key: str, value: str) -> None:
    record = await _get_secret_record(session, key)
    encrypted = encrypt_secret(value)
    if record is None:
        record = SecretRecord(key=key, value=encrypted)
        session.add(record)
    else:
        record.value = encrypted
    await session.commit()


async def delete_secret(session: AsyncSession, key: str) -> None:
    record = await _get_secret_record(session, key)
    if record is None:
        return
    await session.delete(record)
    await session.commit()


def _build_redirect_uri() -> str:
    return f"{settings.public_api_base_url}{settings.token_redirect_path}"


def _create_code_verifier() -> str:
    return secrets.token_urlsafe(72)


def _create_code_challenge(code_verifier: str) -> str:
    digest = hashlib.sha256(code_verifier.encode("utf-8")).digest()
    return base64.urlsafe_b64encode(digest).decode("utf-8").rstrip("=")


async def refresh_onboarding_state(session: AsyncSession) -> SetupState:
    state = await get_setup_state(session)
    llm_configured = await has_secret(session, "llm_api_key") and bool(state.selected_agent_id)
    state.onboarding_completed = (
        state.google_client_configured and state.google_account_connected and llm_configured
    )
    await session.commit()
    await session.refresh(state)
    return state


async def save_google_client_credentials(
    session: AsyncSession,
    client_id: str,
    client_secret: str,
) -> SetupState:
    await upsert_secret(session, "google_client_id", client_id)
    await upsert_secret(session, "google_client_secret", client_secret)
    await delete_secret(session, "google_token_bundle")
    await delete_secret(session, "google_oauth_state")
    await delete_secret(session, "google_oauth_code_verifier")
    state = await get_setup_state(session)
    state.google_client_configured = True
    state.google_account_connected = False
    state.google_email = None
    await session.commit()
    return await refresh_onboarding_state(session)


async def save_llm_credentials(session: AsyncSession, agent_id: str, api_key: str) -> SetupState:
    agent = get_agent_definition(agent_id)
    await upsert_secret(session, "llm_api_key", api_key)
    state = await get_setup_state(session)
    state.selected_agent_id = agent["agent_id"]
    state.llm_provider = agent["provider"]
    state.llm_model = agent["model"]
    await session.commit()
    return await refresh_onboarding_state(session)


async def build_google_auth_url(session: AsyncSession) -> str:
    client_id = await get_secret(session, "google_client_id")
    client_secret = await get_secret(session, "google_client_secret")
    if not client_id or not client_secret:
        raise ValueError("Google OAuth client credentials are not configured yet.")

    state_token = secrets.token_urlsafe(32)
    code_verifier = _create_code_verifier()
    code_challenge = _create_code_challenge(code_verifier)

    await upsert_secret(session, "google_oauth_state", state_token)
    await upsert_secret(session, "google_oauth_code_verifier", code_verifier)

    query = urlencode(
        {
            "client_id": client_id,
            "redirect_uri": _build_redirect_uri(),
            "response_type": "code",
            "scope": " ".join(GOOGLE_SCOPES),
            "access_type": "offline",
            "prompt": "consent",
            "state": state_token,
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
        }
    )
    return f"{GOOGLE_AUTH_ENDPOINT}?{query}"


async def complete_google_oauth_callback(
    session: AsyncSession,
    code: str,
    state_value: str,
) -> SetupState:
    saved_state = await get_secret(session, "google_oauth_state")
    code_verifier = await get_secret(session, "google_oauth_code_verifier")
    client_id = await get_secret(session, "google_client_id")
    client_secret = await get_secret(session, "google_client_secret")

    if not saved_state or not code_verifier or not client_id or not client_secret:
        raise ValueError("OAuth setup is incomplete. Save Google client credentials first.")
    if saved_state != state_value:
        raise ValueError("OAuth state validation failed.")

    async with httpx.AsyncClient(timeout=20.0) as client:
        token_response = await client.post(
            GOOGLE_TOKEN_ENDPOINT,
            data={
                "client_id": client_id,
                "client_secret": client_secret,
                "code": code,
                "code_verifier": code_verifier,
                "grant_type": "authorization_code",
                "redirect_uri": _build_redirect_uri(),
            },
            headers={"Accept": "application/json"},
        )
        token_response.raise_for_status()
        token_payload = token_response.json()

        access_token = token_payload.get("access_token")
        user_email = None
        if access_token:
            userinfo_response = await client.get(
                GOOGLE_USERINFO_ENDPOINT,
                headers={"Authorization": f"Bearer {access_token}"},
            )
            userinfo_response.raise_for_status()
            user_email = userinfo_response.json().get("email")

    await upsert_secret(session, "google_token_bundle", json.dumps(token_payload))
    state = await get_setup_state(session)
    state.google_account_connected = True
    state.google_email = user_email
    await session.commit()
    return await refresh_onboarding_state(session)


async def build_setup_status(session: AsyncSession) -> SetupStatus:
    state = await refresh_onboarding_state(session)
    llm_configured = await has_secret(session, "llm_api_key") and bool(state.selected_agent_id)
    return SetupStatus(
        google_client_configured=state.google_client_configured,
        google_account_connected=state.google_account_connected,
        google_email=state.google_email,
        llm_configured=llm_configured,
        selected_agent_id=state.selected_agent_id,
        onboarding_completed=state.onboarding_completed,
        available_agents=list_agents(),
        gmail_tools=list_gmail_tools(),
    )
