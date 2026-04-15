import os
from pathlib import Path

from cryptography.fernet import Fernet

from api.config import get_settings


settings = get_settings()


def _load_or_create_master_key() -> bytes:
    master_key_path = Path(settings.master_key_path)
    master_key_path.parent.mkdir(parents=True, exist_ok=True)

    if not master_key_path.exists():
        key = Fernet.generate_key()
        master_key_path.write_bytes(key)
        try:
            os.chmod(master_key_path, 0o600)
        except OSError:
            pass

    return master_key_path.read_bytes().strip()


def get_fernet() -> Fernet:
    return Fernet(_load_or_create_master_key())


def encrypt_secret(value: str) -> str:
    return get_fernet().encrypt(value.encode("utf-8")).decode("utf-8")


def decrypt_secret(value: str) -> str:
    return get_fernet().decrypt(value.encode("utf-8")).decode("utf-8")
