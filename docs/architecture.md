# Architecture and build notes

## Runtime components

| Component | Responsibility |
| --- | --- |
| Android/Kotlin foreground service | Starts the recorder, holds the service/wake lock and coordinates Android lifecycle |
| Go engine, bound using gomobile | Google authorization/API requests, event polling, WebRTC and local HTTP server |
| Pion WebRTC | Receives the compatible camera's H.264 RTP stream |
| In-memory pre-roll | Keeps recent video for approximately five seconds before an eligible trigger |
| MP4 writer | Saves playable clips with suitable codec configuration and keyframe boundaries |
| Android motion worker | Decodes frames using MediaCodec for local motion analysis and the admin camera preview |
| Motion detector | Compares sampled luminance in the selected rectangle and submits triggers |
| Android clip preview worker | Decodes recorded clips to create short GIF previews |
| Embedded HTML/CSS/JavaScript | On-phone admin interface and LAN browser gallery |
| Vendored JmDNS module | Advertises local HTTP discovery and responds to supported name lookups |

Google remains in the streaming and authorization path, and in the optional event path.
The phone is the destination for locally saved MP4 files; no Cam Motion Saver recording
backend or cloud-storage account is part of this architecture.

## Event and recording behavior

The existing WebRTC H.264 stream also feeds a bounded queue for an Android
MediaCodec worker. Up to twice per second it samples a 96x54 luminance grid.
The Go detector compares frames inside the persisted normalized rectangle,
compensates for broad brightness shifts and applies one of five sensitivity
levels. It resets its baseline when the area changes. A detected change uses
the existing recording controller and pre-roll. The default area is the whole
frame; the area applies only to phone detection.

The admin preview reuses that stream and updates approximately once per second
when visible; no second camera session is opened. Viewer tokens cannot access
the live-frame endpoint. Decoding and preview processing use phone resources,
and hardware/energy behavior has not been measured on representative devices.

Optional Google events arrive through a dedicated Pub/Sub pull subscription.
The app records raw receipts separately from accepted triggers, rejects malformed
or stale events, and defers eligible events when the stream is not ready.
Phone and Google triggers are independently switchable and both default enabled.

Eligible motion triggers extend an active timed recording; there is also manual start/stop.
Continuous reception is not the same as continuously saving every second to disk.
Stream loss can cause gaps, while codec changes can require another MP4.

Pre-roll is best effort when the receiver is online and has usable codec/keyframe
state. It cannot reconstruct unavailable network footage.

## Storage

Clips and configuration live in app-private storage. Retention is in whole days,
default/minimum one. Completed clips older than the chosen period are removed
with their GIF and metadata sidecars at startup and approximately every minute.
The former fixed 2 GiB eviction cap was removed in 0.5.8.

Longer retention uses more storage. This release does not promise a disk-full
eviction policy, remote backup, Drive synchronization or recovery after uninstall.

## Access boundary

The local HTTP listener uses loopback and the selected private Wi-Fi IPv4
address. It does not bind to a public interface or open a router port.

Viewer and admin credentials are independent. Viewer requests are restricted
server-side to permitted reading/playback resources. Credential configuration,
sharing management and event repair remain available only on the recorder phone.

HTTP traffic is unencrypted. Tokens are access credentials, not TLS.
Discovery publishes service information, not the private sharing tokens.
The old `camfreenest.local` name can remain for update compatibility.

## Reproducing the 0.5.14 build

The recorded toolchain is:

- JDK 17, Go 1.25.1.
- Android SDK Platform 35, Build Tools 35.0.0.
- Android NDK 27.2.12479018.
- Gradle 8.13, Android Gradle Plugin 8.10.1, Kotlin 2.1.20.
- Android API 28 minimum; native targets ARMv7 and ARM64.

The repository includes application source. Run these
commands from the repository root with the required tools installed.

With `JAVA_HOME`, `ANDROID_HOME`, `ANDROID_NDK_HOME`, Go and Gradle configured:

```sh
cd go
go test ./...
go vet ./...
go install golang.org/x/mobile/cmd/gomobile@v0.0.0-20250911085028-6912353760cf
go install golang.org/x/mobile/cmd/gobind@v0.0.0-20250911085028-6912353760cf
gomobile init
mkdir -p ../app/libs
gomobile bind -ldflags=-checklinkname=0 -target=android/arm,android/arm64 -androidapi 28 -o ../app/libs/engine.aar ./engine
cd ..
gradle --no-daemon :mdns:test :app:assembleDebug
```

The `checklinkname` flag is needed by the retained dependency/toolchain
combination; do not silently remove it from reproducible build instructions.

The APK is written to `app/build/outputs/apk/debug/app-debug.apk`.
The retained application ID is `org.camfreenest.android.preview`.
Public source must not include the maintainer's signing keystore. Configure an
appropriate local debug key for development and keep any release key private.
A differently signed build cannot update an already installed maintainer build.

## What has been verified

The 0.5.14 APK passed automated Go/DOM tests, race checks,
Android compilation, APK signature/ZIP/alignment checks and both native ABIs.
The tests include synthetic dim motion, area exclusion and viewer access checks.
Hardware, actual dark footage, touch interaction, energy use, account, network
and event-delivery testing remain distinct checks; the build was not validated
on a physical device during development.
