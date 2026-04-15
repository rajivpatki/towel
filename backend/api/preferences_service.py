from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from api.models import Preference


async def get_all_preferences(session: AsyncSession) -> list[dict]:
    result = await session.execute(select(Preference).order_by(Preference.created_at))
    preferences = result.scalars().all()
    
    return [
        {
            "id": p.id,
            "label": p.title,
            "value": p.content,
            "created_at": p.created_at.isoformat(),
            "updated_at": p.updated_at.isoformat()
        }
        for p in preferences
    ]


async def save_preferences(session: AsyncSession, preferences_data: list[dict]) -> None:
    await session.execute(select(Preference).limit(1))
    
    existing = await session.execute(select(Preference))
    existing_prefs = {p.id: p for p in existing.scalars().all()}
    
    incoming_ids = {p["id"] for p in preferences_data if p.get("id")}
    
    for pref_id in set(existing_prefs.keys()) - incoming_ids:
        await session.delete(existing_prefs[pref_id])
    
    for pref_data in preferences_data:
        pref_id = pref_data.get("id")
        value = pref_data.get("value", "").strip()
        
        if not value:
            continue
        
        if pref_id and pref_id in existing_prefs:
            existing_prefs[pref_id].content = value
            existing_prefs[pref_id].title = value[:100]
        else:
            new_pref = Preference(
                title=value[:100],
                content=value
            )
            session.add(new_pref)
    
    await session.commit()
