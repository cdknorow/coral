# Remote access

Remote access lets another device, such as your phone, reach the dashboard over your network. **It is off by default.** Until you turn it on and restart, Coral listens on `127.0.0.1` only and accepts no direct network connections from other machines. A tunnel, proxy or port forward that you set up yourself can still expose that local listener.

## Saved and effective settings

Two values are reported separately:

- **Saved**: the `remote_access_enabled` preference stored in Coral's settings.
- **Effective**: whether the running server is listening on a non-loopback address.

Changes take effect only on restart. Between saving and restarting the two differ, and the app shows a pending state (`enable_pending_restart` or `disable_pending_restart`).

## Turn it on

1. Open the mobile connection (QR) dialog or **Settings → Privacy**.
2. If access is off, the dialog asks **"Mobile access is disabled. Enable it?"**. Opening or cancelling changes nothing. Choose **Enable** to save the preference.
3. **Restart Coral.** The QR code and API key stay hidden until both the saved and the running state are enabled.
4. Scan the QR code or open the shown URL from a device on the same network.

If the saved setting is missing, set to anything other than `true`, or cannot be read at startup, Coral binds to loopback. This also applies if you pass `--host 0.0.0.0` or set `CORAL_HOST`: the startup log says the bind was restricted and why.

To turn it off, clear the setting and restart. Existing remote connections are not closed until the restart.

## Authentication

Requests from the same machine (localhost) are accepted without a key. Other devices must present Coral's API key, which is generated on first run and shown with the QR code. Treat it like a password. Coral does not classify clients using forwarded headers or `Host` values.

## What this does and does not cover

- Coral controls its own listener. A reverse proxy, SSH tunnel or port forward that you set up separately can still expose a loopback listener. Disable those yourself.
- The direct listener serves plain HTTP, not HTTPS. Use it on a network you trust or put your own encrypted tunnel in front of it.
- Turning remote access off does not change agent permission or sandbox settings.
- Webhooks are outbound requests and are not affected by this setting.

For the status and setting names used by the API, see `GET /api/system/privacy`.
