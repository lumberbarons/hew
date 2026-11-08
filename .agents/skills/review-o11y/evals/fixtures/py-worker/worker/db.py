from typing import Any


class Database:
    def __init__(self, url: str):
        self.url = url

    def get_user(self, user_id: str) -> dict:
        return {"id": user_id}

    def save_result(self, user_id: str, result: Any) -> None:
        pass

    def claim_jobs(self, batch_size: int) -> list[dict]:
        return []
