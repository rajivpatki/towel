from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, HTTPException, Query
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import HTMLResponse
from sqlalchemy.ext.asyncio import AsyncSession

from api.chat_service import process_chat_message
from api.config import get_settings
from api.db import get_db_session, init_db
from api.gmail_tools import list_gmail_tools
from api.history_service import get_action_history
from api.preferences_service import get_all_preferences, save_preferences
from api.schemas import (
    ChatMessageIn,
    ChatMessageOut,
    GoogleAuthUrlOut,
    GoogleOAuthCredentialsIn,
    HistoryListOut,
    LLMSetupIn,
    PreferencesIn,
    PreferencesOut,
    SetupStatus,
    SuccessResponse,
)
from api.setup_service import (
    build_google_auth_url,
    build_setup_status,
    complete_google_oauth_callback,
    save_google_client_credentials,
    save_llm_credentials,
)

settings = get_settings()


@asynccontextmanager
async def lifespan(_: FastAPI):
    await init_db()
    yield


app = FastAPI(title=settings.app_name, lifespan=lifespan)
app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.parsed_cors_origins(),
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.get("/")
async def root() -> dict[str, str]:
    return {"name": settings.app_name, "status": "ok"}


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "healthy"}


@app.get(f"{settings.api_prefix}/setup/status", response_model=SetupStatus)
async def get_setup_status(session: AsyncSession = Depends(get_db_session)) -> SetupStatus:
    return await build_setup_status(session)


@app.post(f"{settings.api_prefix}/setup/google/oauth-credentials", response_model=SuccessResponse)
async def save_google_credentials(
    payload: GoogleOAuthCredentialsIn,
    session: AsyncSession = Depends(get_db_session),
) -> SuccessResponse:
    await save_google_client_credentials(session, payload.client_id, payload.client_secret)
    return SuccessResponse()


@app.post(f"{settings.api_prefix}/setup/google/connect", response_model=GoogleAuthUrlOut)
async def connect_google_account(session: AsyncSession = Depends(get_db_session)) -> GoogleAuthUrlOut:
    try:
        auth_url = await build_google_auth_url(session)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    return GoogleAuthUrlOut(auth_url=auth_url)


@app.get(f"{settings.api_prefix}/setup/google/callback", response_class=HTMLResponse)
async def google_callback(
    code: str = Query(...),
    state: str = Query(...),
    session: AsyncSession = Depends(get_db_session),
) -> HTMLResponse:
    frontend_url = "http://localhost:3000/setup/gmail"
    try:
        await complete_google_oauth_callback(session, code, state)
        # Redirect back to frontend with success
        body = f"""<!DOCTYPE html>
<html>
<head>
    <meta http-equiv="refresh" content="0; url={frontend_url}?oauth=success">
    <script>window.location.href = "{frontend_url}?oauth=success";</script>
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'SF Pro Text',Arial,sans-serif;background:#f5f5f7;color:#1d1d1f;padding:40px;text-align:center;">
    <h2>Connected successfully</h2>
    <p>Redirecting back to setup...</p>
</body>
</html>"""
        return HTMLResponse(content=body)
    except Exception as exc:
        error_msg = str(exc).replace('"', '&quot;')
        body = f"""<!DOCTYPE html>
<html>
<head>
    <meta http-equiv="refresh" content="3; url={frontend_url}?oauth=error&msg={error_msg}">
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'SF Pro Text',Arial,sans-serif;background:#ffebe9;color:#ff3b30;padding:40px;text-align:center;">
    <h2>Connection failed</h2>
    <p>{error_msg}</p>
    <p>Redirecting back...</p>
</body>
</html>"""
        return HTMLResponse(content=body, status_code=400)


@app.post(f"{settings.api_prefix}/setup/llm", response_model=SuccessResponse)
async def save_llm_setup(
    payload: LLMSetupIn,
    session: AsyncSession = Depends(get_db_session),
) -> SuccessResponse:
    try:
        await save_llm_credentials(session, payload.agent_id, payload.api_key)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    return SuccessResponse()


@app.get(f"{settings.api_prefix}/tools/gmail")
async def get_gmail_tools() -> list[dict]:
    return list_gmail_tools()


@app.post(f"{settings.api_prefix}/chat", response_model=ChatMessageOut)
async def chat_endpoint(
    payload: ChatMessageIn,
    session: AsyncSession = Depends(get_db_session),
) -> ChatMessageOut:
    try:
        result = await process_chat_message(session, payload.message)
        return ChatMessageOut(**result)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        raise HTTPException(status_code=500, detail=f"Chat processing error: {str(exc)}") from exc


@app.get(f"{settings.api_prefix}/history", response_model=HistoryListOut)
async def get_history(session: AsyncSession = Depends(get_db_session)) -> HistoryListOut:
    items = await get_action_history(session)
    return HistoryListOut(items=items)


@app.get(f"{settings.api_prefix}/preferences", response_model=PreferencesOut)
async def get_preferences(session: AsyncSession = Depends(get_db_session)) -> PreferencesOut:
    preferences = await get_all_preferences(session)
    return PreferencesOut(preferences=preferences)


@app.post(f"{settings.api_prefix}/preferences", response_model=SuccessResponse)
async def update_preferences(
    payload: PreferencesIn,
    session: AsyncSession = Depends(get_db_session),
) -> SuccessResponse:
    try:
        await save_preferences(session, [p.model_dump() for p in payload.preferences])
        return SuccessResponse()
    except Exception as exc:
        raise HTTPException(status_code=500, detail=f"Failed to save preferences: {str(exc)}") from exc
