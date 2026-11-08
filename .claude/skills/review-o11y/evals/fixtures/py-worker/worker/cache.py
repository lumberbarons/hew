from typing import Any


class Cache:
    def __init__(self, url: str):
        self.url = url

    def get(self, key: str) -> Any:
        return None

    def set(self, key: str, value: Any) -> None:
        pass
