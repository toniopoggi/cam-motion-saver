# Troubleshooting

## APK will not install or update

Check Android 9/API 28 minimum, ARMv7/ARM64 support and available space.
Record the actual Android installer error.

An update must use the same application ID and signing certificate as the
installed build, with a permitted version code. A self-built APK signed with a
different key cannot update the maintained preview.

Do not uninstall as the first troubleshooting step: uninstalling removes the
app-private recordings and configuration. Back up downloadable clips first.

## Stream works, but no motion recordings appear

Check that the recorder and phone motion detection are enabled, the stream is
connected, and motion occurs inside the saved area. Try a higher sensitivity
or select the whole image; check the phone detector status for decoder errors.
Dark/noisy footage and small motion may still be missed, while higher
sensitivity may cause false triggers. Test with real footage.

If also using Google event triggers, inspect **Recorder status & diagnostics**
and separate these stages:

| Signal | What it establishes |
| --- | --- |
| Nest access verified | The device API call succeeded |
| WebRTC online/buffer ready | Video reception is working |
| Subscription check succeeds | Subscription configuration/access checks succeeded |
| Successful pull count rises | The recorder can complete Pub/Sub pulls |
| Raw message count rises | Pub/Sub actually delivered messages |
| Trigger count rises | The app accepted an event for recording |
| MP4 appears and plays | The recording path produced usable output |

If Google event triggers are enabled and raw receipts remain zero, check the event topic and dedicated pull
subscription. After authorization changes, request **Reconnect event delivery**
and inspect activation/pull errors. Trigger a fresh event after the stream is ready.

Do not run two recorders against the same subscription: consumers compete for
messages. Use independent subscriptions when independent consumers need every event.

If receipts rise but triggers do not, inspect rejection reasons and the phone's
clock. Old, malformed or duplicate events are different from no delivered events.
A paused recorder intentionally stops polling.

A credentials check or activation request alone does not prove that Google is
publishing camera events.

## Clips end while movement remains visible

Phone detection analyses sampled frames and may not trigger on every visible
movement. Google may also deliver events sparsely or not at all. New eligible
triggers extend the deadline. Adjust the area and sensitivity, and increase
**Motion clip duration** as appropriate (5–600 seconds). Stream interruptions
and codec changes can also split footage.

Do not confuse clip duration with **Keep recordings for**, which controls how
many days completed files remain on storage.

## Camera preview or motion area does not work

The admin preview updates about once per second when its section is visible.
Ensure the recorder is running and the stream is connected. Some Android hardware
decoders cannot expose the required frames; inspect the phone detector status.
The viewer-only link cannot access the live preview or change the area.

## Retention or storage surprises

Default/minimum retention is one day, rolling 24 hours. Enter a whole number
of days and save. Seven days and 30 days are fixed elapsed periods.

Cleanup runs approximately once per minute. Lowering the value expires old
completed files at the next cleanup. The current version has no fixed 2 GiB
deletion cap. If storage fills, download/archive recordings or reduce retention;
cloud backup and automatic disk-full eviction are not implemented.

## GIF preview is missing

The video can remain playable without a GIF. A short or damaged clip may have
too few decoded frames. One usable frame can produce a still preview; no usable
frames should produce an explicit failure. Admin access offers **Retry preview**
when available. Check logs for decoder errors rather than assuming the MP4 was lost.

## IP works, but the hostname does not

Use the complete IP-based link shown by the phone. Verify the same non-guest
Wi-Fi/LAN, local peer access and multicast support. Access by IP and resolution
of `.local` names are separate operations.

Use the actual displayed hostname, including a collision suffix. The name can
remain `camfreenest.local` after the app rename.
Diagnostics on the phone show announcement/query/reply information.

Do not add DDNS, public port forwarding or disable router protections merely
to make local discovery work.

## A sharing link does not open

Copy the full link from **Share**, including its token. Viewer and admin links
are not interchangeable. Resetting the viewer link invalidates old viewer links
and browser sessions. A lost/removed query token does not grant anonymous access.

A browser paired earlier may have a cookie, so test with a fresh browser session
when checking access restrictions. Do not post complete token-bearing links in issues.

## Google authorization fails after about seven days

Consult Google's current [authorization errors](https://developers.google.com/nest/device-access/reference/errors/authorization)
and the OAuth project's Testing/Production status. After an appropriate status
change, authorize again to obtain a fresh token.

Production status does not make credentials permanent or approve commercial
Device Access. Revocation and other token-expiry reasons still apply.

## Report a problem

Include version, Android model/version, camera model, whether private-IP access
works, and redacted counters/errors. Exclude credentials, service-account JSON
keys, sharing tokens and identifiable video unless deliberately sharing it.
