"""Tests for coral.messageboard.subgent_client — Subgent admin API client."""

import ipaddress
from unittest.mock import MagicMock, patch

import httpx
import pytest

from coral.messageboard.subgent_client import (
    _is_ip_blocked,
    create_agent_key,
    ensure_board,
    ensure_project,
    list_keys,
    revoke_key,
    validate_url,
)


# ── _is_ip_blocked tests ─────────────────────────────────────────────────────


class TestIsIpBlocked:
    def test_loopback_ipv4_allowed(self):
        assert _is_ip_blocked(ipaddress.ip_address("127.0.0.1")) is False

    def test_loopback_ipv6_allowed(self):
        assert _is_ip_blocked(ipaddress.ip_address("::1")) is False

    def test_private_10_range(self):
        assert _is_ip_blocked(ipaddress.ip_address("10.0.0.1")) is True

    def test_private_172_range(self):
        assert _is_ip_blocked(ipaddress.ip_address("172.16.0.1")) is True

    def test_private_192_range(self):
        assert _is_ip_blocked(ipaddress.ip_address("192.168.1.1")) is True

    def test_link_local(self):
        assert _is_ip_blocked(ipaddress.ip_address("169.254.1.1")) is True

    def test_cgnat_range(self):
        assert _is_ip_blocked(ipaddress.ip_address("100.64.0.1")) is True

    def test_cgnat_upper_bound(self):
        assert _is_ip_blocked(ipaddress.ip_address("100.127.255.255")) is True

    def test_ipv6_mapped_ipv4_loopback_allowed(self):
        """::ffff:127.0.0.1 should be allowed (loopback is permitted)."""
        assert _is_ip_blocked(ipaddress.ip_address("::ffff:127.0.0.1")) is False

    def test_ipv6_mapped_ipv4_private(self):
        assert _is_ip_blocked(ipaddress.ip_address("::ffff:10.0.0.1")) is True

    def test_public_ipv4(self):
        assert _is_ip_blocked(ipaddress.ip_address("8.8.8.8")) is False

    def test_public_ipv6(self):
        assert _is_ip_blocked(ipaddress.ip_address("2606:4700::1")) is False


# ── validate_url tests ────────────────────────────────────────────────────────


class TestValidateUrl:
    def test_rejects_ftp_scheme(self):
        with pytest.raises(ValueError, match="scheme"):
            validate_url("ftp://example.com")

    def test_rejects_file_scheme(self):
        with pytest.raises(ValueError, match="scheme"):
            validate_url("file:///etc/passwd")

    def test_rejects_no_hostname(self):
        with pytest.raises(ValueError, match="hostname"):
            validate_url("http://")

    @patch("coral.messageboard.subgent_client.socket.getaddrinfo")
    def test_rejects_private_ip(self, mock_dns):
        mock_dns.return_value = [
            (2, 1, 6, "", ("10.0.0.1", 80)),
        ]
        with pytest.raises(ValueError, match="blocked IP"):
            validate_url("http://evil.example.com")

    @patch("coral.messageboard.subgent_client.socket.getaddrinfo")
    def test_accepts_public_ip(self, mock_dns):
        mock_dns.return_value = [
            (2, 1, 6, "", ("93.184.216.34", 443)),
        ]
        result = validate_url("https://example.com/")
        assert result == "https://example.com"  # trailing slash stripped

    @patch("coral.messageboard.subgent_client.socket.getaddrinfo")
    def test_strips_trailing_slash(self, mock_dns):
        mock_dns.return_value = [
            (2, 1, 6, "", ("93.184.216.34", 443)),
        ]
        assert validate_url("https://example.com///") == "https://example.com"

    def test_rejects_unresolvable_hostname(self):
        with pytest.raises(ValueError, match="resolve"):
            validate_url("http://this-will-never-resolve-1234567890.invalid")


# ── create_agent_key tests ────────────────────────────────────────────────────


class TestCreateAgentKey:
    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_correct_payload(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.json.return_value = {"key": "cb_agent_xxx", "id": 1}
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.post.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        result = create_agent_key(
            admin_url="https://subgent.io",
            admin_key="cb_live_admin",
            org_id="acme",
            board="backend",
            session_id="agent-1",
            job_title="Dev",
        )

        mock_client.post.assert_called_once()
        call_args = mock_client.post.call_args
        assert call_args[0][0] == "https://subgent.io/api/board/admin/agent-keys"
        payload = call_args[1]["json"]
        assert payload["org_id"] == "acme"
        assert payload["board"] == "backend"
        assert payload["session_id"] == "agent-1"
        assert payload["job_title"] == "Dev"
        assert payload["scopes"] == "read,write"
        assert payload["check_mode"] == "all"
        assert payload["label"] == "coral-agent-1"  # default label
        assert call_args[1]["headers"]["Authorization"] == "Bearer cb_live_admin"
        assert result == {"key": "cb_agent_xxx", "id": 1}

    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_custom_label_and_scopes(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.json.return_value = {"key": "cb_agent_yyy"}
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.post.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        create_agent_key(
            admin_url="https://subgent.io",
            admin_key="key",
            org_id="org",
            board="b",
            session_id="s",
            job_title="j",
            label="My Label",
            scopes="read",
        )

        payload = mock_client.post.call_args[1]["json"]
        assert payload["label"] == "My Label"
        assert payload["scopes"] == "read"

    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_includes_webhook_url(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.json.return_value = {"key": "cb_agent_zzz"}
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.post.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        create_agent_key(
            admin_url="https://subgent.io",
            admin_key="key",
            org_id="org",
            board="b",
            session_id="s",
            job_title="j",
            webhook_url="https://hooks.example.com/notify",
        )

        payload = mock_client.post.call_args[1]["json"]
        assert payload["webhook_url"] == "https://hooks.example.com/notify"

    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_raises_on_http_error(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.raise_for_status.side_effect = httpx.HTTPStatusError(
            "Bad Request", request=MagicMock(), response=MagicMock(status_code=400)
        )
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.post.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        with pytest.raises(httpx.HTTPStatusError):
            create_agent_key(
                admin_url="https://subgent.io",
                admin_key="key",
                org_id="org",
                board="b",
                session_id="s",
                job_title="j",
            )

    def test_raises_on_ssrf(self):
        with pytest.raises(ValueError):
            create_agent_key(
                admin_url="http://10.0.0.1:8420",
                admin_key="key",
                org_id="org",
                board="b",
                session_id="s",
                job_title="j",
            )


# ── revoke_key tests ──────────────────────────────────────────────────────────


class TestRevokeKey:
    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_correct_url_and_method(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.json.return_value = {"ok": True}
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.delete.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        result = revoke_key("https://subgent.io", "cb_live_admin", "42")

        mock_client.delete.assert_called_once()
        call_args = mock_client.delete.call_args
        assert call_args[0][0] == "https://subgent.io/api/board/admin/keys/42"
        assert call_args[1]["headers"]["Authorization"] == "Bearer cb_live_admin"
        assert result == {"ok": True}

    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_raises_on_404(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.raise_for_status.side_effect = httpx.HTTPStatusError(
            "Not Found", request=MagicMock(), response=MagicMock(status_code=404)
        )
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.delete.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        with pytest.raises(httpx.HTTPStatusError):
            revoke_key("https://subgent.io", "key", "999")


# ── list_keys tests ───────────────────────────────────────────────────────────


class TestListKeys:
    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_correct_url_and_params(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.json.return_value = [{"id": 1}, {"id": 2}]
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.get.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        result = list_keys("https://subgent.io", "cb_live_admin", "acme")

        mock_client.get.assert_called_once()
        call_args = mock_client.get.call_args
        assert call_args[0][0] == "https://subgent.io/api/board/admin/keys"
        assert call_args[1]["params"] == {"org_id": "acme"}
        assert call_args[1]["headers"]["Authorization"] == "Bearer cb_live_admin"
        assert result == [{"id": 1}, {"id": 2}]

    @patch("coral.messageboard.subgent_client.validate_url", return_value="https://subgent.io")
    @patch("coral.messageboard.subgent_client.httpx.Client")
    def test_raises_on_403(self, mock_client_cls, mock_validate):
        mock_resp = MagicMock()
        mock_resp.raise_for_status.side_effect = httpx.HTTPStatusError(
            "Forbidden", request=MagicMock(), response=MagicMock(status_code=403)
        )
        mock_client = MagicMock()
        mock_client.__enter__ = MagicMock(return_value=mock_client)
        mock_client.__exit__ = MagicMock(return_value=False)
        mock_client.get.return_value = mock_resp
        mock_client_cls.return_value = mock_client

        with pytest.raises(httpx.HTTPStatusError):
            list_keys("https://subgent.io", "bad_key", "acme")
