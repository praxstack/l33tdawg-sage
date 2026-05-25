"""SAGE SDK exceptions."""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import httpx


class SageError(Exception):
    """Base exception for all SAGE SDK errors."""


class SageAuthError(SageError):
    """Authentication or signing error."""


class SageAPIError(SageError):
    """Error returned by the SAGE API."""

    def __init__(
        self,
        status_code: int,
        detail: str,
        error_type: str | None = None,
    ) -> None:
        self.status_code = status_code
        self.detail = detail
        self.error_type = error_type
        super().__init__(f"HTTP {status_code}: {detail}")

    @classmethod
    def from_response(cls, response: httpx.Response) -> SageAPIError:
        """Parse an RFC 7807 Problem Details response into the appropriate error."""
        status_code = response.status_code
        try:
            body = response.json()
            detail = body.get("detail", response.text)
            error_type = body.get("type")
            title = body.get("title", "")
        except Exception:
            detail = response.text
            error_type = None
            title = ""

        if status_code == 401 or status_code == 403:
            raise SageAuthError(detail)
        if status_code == 404:
            return SageNotFoundError(
                status_code=status_code, detail=detail, error_type=error_type
            )
        if status_code == 422:
            return SageValidationError(
                status_code=status_code, detail=detail, error_type=error_type
            )
        return cls(status_code=status_code, detail=detail, error_type=error_type)


class SageNotFoundError(SageAPIError):
    """Resource not found (404)."""


class SageValidationError(SageAPIError):
    """Validation error (422)."""


# --- ABCI error code surface notes ---
#
# The exception hierarchy is intentionally flat — the SDK doesn't map every
# ABCI error code to a dedicated class. Useful ones to know when handling
# :class:`SageAPIError` from the v8.0 domain reassign endpoints:
#
#   Code 13 — generic permission denied (HTTP 403 / SageAuthError).
#   Code 50 — "shared domain not ownable": surfaced as HTTP 403 with a
#             detail string containing ``shared domain not ownable``. Raised
#             when an agent attempts ``register_domain`` or
#             ``submit_domain_reassign`` against a domain that the chain has
#             marked as shared (``open_to_shared=true``). Callers that want
#             to special-case Code 50 should ``str.contains`` on the detail.
#
# This block is documentation only — no class is added because there is no
# existing per-code mapping infrastructure to extend.
