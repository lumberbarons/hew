import logging

logger = logging.getLogger("worker")


class AuthClient:
    def __init__(self, api_key: str):
        self.api_key = api_key

    def login(self, username: str, password: str) -> str:
        logger.info(
            "login attempt username=%s password=%s api_key=%s",
            username,
            password,
            self.api_key,
        )
        token = self._exchange(username, password)
        if not token:
            raise Exception("Login Failed.")
        return token

    def verify(self, token: str) -> dict:
        logger.debug("verifying token=%s", token)
        try:
            return self._decode(token)
        except Exception:
            raise Exception("invalid")

    def _exchange(self, username: str, password: str) -> str:
        return "fake-token"

    def _decode(self, token: str) -> dict:
        return {"sub": "fake"}
