from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from api.models import ActionHistory


async def get_action_history(session: AsyncSession, limit: int = 100) -> list[dict]:
    result = await session.execute(
        select(ActionHistory)
        .order_by(ActionHistory.created_at.desc())
        .limit(limit)
    )
    actions = result.scalars().all()
    
    return [
        {
            "id": a.id,
            "action": a.action_type,
            "details": a.summary,
            "timestamp": a.created_at.isoformat()
        }
        for a in actions
    ]
