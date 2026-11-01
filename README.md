# Cam Motion Saver

**Record video from a compatible Google Nest camera on an Android phone. The phone handles motion detection, storage and serves a local web gallery; its own camera is never used.**

Cam Motion Saver is a free app that receives the Nest camera's video stream, detects motion in that stream, saves MP4 clips on the phone and serves a recording gallery to browsers on your local network. It does not require a Google Home Premium subscription.
The Nest camera captures the video. The phone supplies the processor, storage and server: it does not need a working camera and does not need to be positioned near or pointed at the scene. Keep it somewhere convenient with power, Wi-Fi and internet access. Reuse hardware you already own without adding a dedicated recording computer or hosted recording service.

## Features

- Motion detection on the phone using the Nest video stream, with five sensitivity levels and an adjustable rectangular detection area.
- Optional Google camera event triggers via Pub/Sub, independently enabled or disabled.
- Approximately five seconds of pre-motion video when the stream and buffer are ready.
- Motion-triggered, manual and timed MP4 recordings, preserving the incoming H.264 pictures without re-encoding.
- Clip duration from 5 to 600 seconds; subsequent eligible triggers extend the recording.
- Local retention in whole days, with automatic expiry of completed clips and previews.
- Animated previews, browser playback, seeking and MP4 downloads.
- Separate viewer and administrator links, with server-enforced permissions.
- Local hostname discovery with a private-IP fallback.

## Requirements

| Component | Requirement |
| --- | --- |
| Phone | Android 9/API 28 or newer; ARMv7 or ARM64; its own camera is not required |
| Decoder | Compatible hardware H.264 decoder exposing frames for phone detection |
| Camera | A Google Nest camera exposing WebRTC/H.264 through SDM |
| Storage | Sufficient free phone storage for the chosen retention |
| Network | Internet for the camera stream and Google APIs; trusted LAN for browser viewing |
| Google setup | Your own Device Access project and OAuth client |
| Optional Google events | Dedicated Pub/Sub pull subscription and service-account credentials |

RTSP-only cameras are not supported by this build. Check the stream protocol for your exact device and its Nest/Home migration state. Use a phone, battery and charger suitable for sustained operation.

## Cost and connectivity

The app is free and has no Cam Motion Saver account, subscription or hosted storage charge. Google Home Premium is not required.

Google's separate Device Access programme currently charges a [one-time US$5 registration fee](https://developers.google.com/nest/device-access/registration). Google Cloud usage is subject to Google's quotas and applicable charges. Electricity and storage are provided by your phone.

Recordings are local, but the Nest stream and account authorisation still depend on Google's services and internet connectivity.

## Install

The supplied build is **0.5.14-preview**, the version currently used by the author. Its existing preview package identity is retained for installation continuity.

| Field | Value |
| --- | --- |
| Application ID | `org.camfreenest.android.preview` |
| Version code | `64` |
| Minimum Android | 9 / API 28 |
| Native ABIs | ARMv7, ARM64 |
| Local HTTP port | `8767` |

Install the APK on the recording phone. Existing users should install the update without uninstalling or clearing data. Updates require the same application ID and signing certificate. Uninstalling removes app-private settings and recordings.

APK SHA-256:

```text
107feed7f3656c4695a8229781f71ec2157b84e1d9c2a90e7e5c7547ec9eb476
```

## First run

1. Complete the [Google setup](docs/setup.md), using your own projects and credentials.
2. Open **Camera & Google settings** on the phone, enter the configuration and authorise the camera account.
3. Verify Nest access and wait for the WebRTC stream and pre-motion buffer to become ready.
4. Under **Live view & motion**, enable phone detection, select sensitivity and draw/save the detection area.
5. Move within the area and confirm a new MP4 appears and plays.
6. Set **Motion clip duration** and **Keep recordings for**.
7. Copy the viewer link from **Share** to a browser on the same trusted LAN.

Google event triggers are optional. Configure their dedicated Pub/Sub pull subscription separately if needed. A passing subscription check alone does not establish that motion messages are being delivered.

If Android stops the background recorder, check battery optimisation settings. See [troubleshooting](docs/troubleshooting.md) for diagnostics.

## Recording and retention

The receiver maintains recent encoded video in memory. Triggers save available pre-roll followed by the configured recording duration. Further eligible triggers extend the deadline. Reconnection or actual video format changes may split recordings; unavailable footage cannot be recovered.

Retention defaults to one rolling day and accepts another whole number of days. Cleanup runs at startup and approximately every minute, deleting expired completed clips and their previews. Lowering retention removes older clips at the next cleanup. Active partial recordings are excluded.

There is no fixed 2 GiB deletion cap or automatic disk-full eviction. Keep sufficient free space and download important recordings. Cloud backup and Drive synchronisation are not included.

## Viewer and administrator access

Viewer links grant browsing, playback and downloads. The separate administrator link also grants recording controls and recording settings. Google credentials and sharing management remain available only on the recording phone.

Use the address displayed by the app. If `.local` resolution fails, use the numeric private-IP link. The retained hostname may include `camfreenest` for compatibility with existing installations.

The viewer uses unencrypted HTTP. Keep links private, use a trusted LAN and do not expose port 8767 through public forwarding or tunnels.

## Architecture and development

```text
Nest camera -> Google-authorised WebRTC/H.264 -> Android phone
                                               |-- pre-motion buffer
                                               |-- frame sampling and motion detection
                                               |-- MP4 storage and previews
                                               |-- retention cleanup
                                               `-- HTTP gallery -> LAN browser
Optional Google events -> Pub/Sub --------------> recording trigger
```

The application combines Kotlin, an Android foreground service, a Go engine bound with gomobile, Pion WebRTC, Android MediaCodec, an embedded web interface and a vendored JmDNS module.

See [architecture and build instructions](docs/architecture.md). To run Go checks:

```sh
cd go
go test ./...
go vet ./...
```

Build the Go binding and Android APK using the linked build instructions. Development builds use the builder's standard Android debug key. The maintainer's signing key is not included; a differently signed development APK cannot update the maintainer-signed installation.

The original 0.5.14 build report records passing Go, race, vet, simulated UI, Android compilation and APK checks. The author reports successful use on their phone. This does not establish compatibility with every camera or Android device.

## Limitations and Google integration

Motion detection compares changes in sampled frames; it does not recognise people or animals. Shadows, lighting, vegetation and compression can produce false or missed triggers. Tune the area and sensitivity for your view.

This is an independent project, unaffiliated with Google. Google's current [commercial Device Access review guidelines](https://developers.google.com/nest/device-access/project/review) reject integrations that store SDM video. Cam Motion Saver is not represented as a Google-approved commercial integration. Review the applicable programme terms for your use.

## Reporting issues

Include the app version, phone model, Android version, camera model and redacted diagnostics. Distinguish stream connectivity, phone detection and optional Google event delivery. Do not post credentials, full sharing links or identifiable footage unintentionally.

## The story behind the app

Read [An old Android phone becomes a Nest camera recorder](https://antoniopoggi.com/writing/camsaver-android-nest-recorder/) for the longer account of reusing a phone, choosing the architecture and working through the recording and preview problems.

## Licence

MIT. See [LICENSE](LICENSE) and preserve the existing attribution and third-party notices, including [JmDNS's licence](mdns/LICENSE).
