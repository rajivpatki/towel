from functools import lru_cache

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    app_name: str = "Towel"
    api_prefix: str = "/api"
    database_url: str = "sqlite+aiosqlite:////data/towel.db"
    data_dir: str = "/data"
    public_api_base_url: str = "http://localhost:8000"
    cors_origins: str = Field(
        default="http://localhost:3000,http://127.0.0.1:3000"
    )

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
    )

    @property
    def master_key_path(self) -> str:
        return f"{self.data_dir}/secrets/master.key"

    @property
    def token_redirect_path(self) -> str:
        return f"{self.api_prefix}/setup/google/callback"

    def parsed_cors_origins(self) -> list[str]:
        return [origin.strip() for origin in self.cors_origins.split(",") if origin.strip()]


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    return Settings()
