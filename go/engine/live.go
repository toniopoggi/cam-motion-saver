package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"camfreenest/capture"
	"camfreenest/mp4lite"
	"camfreenest/preroll"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

func validCamera(c Configuration) bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != "" && strings.HasPrefix(c.DeviceName, "enterprises/") && strings.Contains(c.DeviceName, "/devices/")
}

func decodeH264FMTP(fmtp string) ([]byte, []byte) {
	for _, kv := range strings.Split(fmtp, ";") {
		kv = strings.TrimSpace(kv)
		if !strings.HasPrefix(strings.ToLower(kv), "sprop-parameter-sets=") {
			continue
		}
		pairs := strings.SplitN(strings.SplitN(kv, "=", 2)[1], ",", 2)
		if len(pairs) != 2 {
			continue
		}
		sps, e := base64.StdEncoding.DecodeString(strings.TrimSpace(pairs[0]))
		if e != nil {
			continue
		}
		pps, e := base64.StdEncoding.DecodeString(strings.TrimSpace(pairs[1]))
		if e != nil {
			continue
		}
		return mp4lite.CanonicalParameterSet(sps), mp4lite.CanonicalParameterSet(pps)
	}
	return nil, nil
}

func (r *Runner) liveLoop(ctx context.Context) {
	c := r.loadConfig()
	controller, err := capture.New(r.dir+"/clips", 5*time.Second, time.Duration(c.ClipSeconds)*time.Second,
		func(name string, err error) {
			if err != nil {
				r.log("error", "Recording failed: "+err.Error())
			} else {
				detail := ""
				if b, e := os.ReadFile(filepath.Join(r.dir, "clips", name+".meta.json")); e == nil {
					var m capture.ClipMetadata
					if json.Unmarshal(b, &m) == nil {
						detail = fmt.Sprintf(" (%.1fs; %s)", m.DurationSeconds, m.EndReason)
					}
				}
				r.log("saved", "Saved "+name+detail)
			}
		}, func(writing bool) { r.busy.Store(writing) })
	if err != nil {
		r.log("error", err.Error())
		return
	}
	r.captureMu.Lock()
	r.captureController = controller
	r.captureMu.Unlock()
	defer func() { controller.Close(); r.captureMu.Lock(); r.captureController = nil; r.captureMu.Unlock() }()
	for ctx.Err() == nil {
		if !r.recorderEnabled.Load() {
			controller.Offline()
			if !pause(ctx, time.Second) {
				return
			}
			continue
		}
		c = r.loadConfig()
		_ = controller.SetDuration(time.Duration(c.ClipSeconds) * time.Second)
		if !validCamera(c) {
			if !pause(ctx, 2*time.Second) {
				return
			}
			continue
		}
		err = r.runLiveSession(ctx, c, controller)
		controller.Suspend()
		if ctx.Err() != nil {
			return
		}
		current := r.loadConfig()
		if current.DeviceName != c.DeviceName {
			controller.Offline()
			r.manualWanted.Store(false)
		}
		if err != nil {
			r.log("error", "Stream interrupted; unfinished MP4 is retained for reconnect: "+err.Error())
		}
		if !pause(ctx, time.Second) {
			return
		}
	}
}
func (r *Runner) runLiveSession(parent context.Context, c Configuration, controller *capture.Controller) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	r.sessionMu.Lock()
	r.sessionCancel = cancel
	if !r.recorderEnabled.Load() {
		cancel()
	}
	r.sessionMu.Unlock()
	defer func() { r.sessionMu.Lock(); r.sessionCancel = nil; r.sessionMu.Unlock() }()
	token, err := accessToken(c)
	if err != nil {
		return err
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}})
	if err != nil {
		return err
	}
	defer peer.Close()
	if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return err
	}
	if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		return err
	}
	if _, err = peer.CreateDataChannel("camfreenest-control", nil); err != nil {
		return err
	}

	epoch := uint64(time.Now().UnixNano())
	ended := make(chan error, 1)
	var endOnce sync.Once
	signalEnd := func(err error) { endOnce.Do(func() { ended <- err }) }
	peer.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateDisconnected {
			r.log("info", "ICE temporarily disconnected; retaining current recording")
		}

		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			signalEnd(fmt.Errorf("peer state %s", s.String()))
		}
	})
	peer.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if ctx.Err() != nil || !r.recorderEnabled.Load() {
			return
		}
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			go func() {
				for {
					if _, _, e := track.ReadRTP(); e != nil {
						return
					}
				}
			}()
			return
		}
		if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeH264) {
			signalEnd(fmt.Errorf("Nest negotiated %s not H264", track.Codec().MimeType))
			return
		}
		controller.BeginEpoch(epoch)
		sps, pps := decodeH264FMTP(track.Codec().SDPFmtpLine)
		controller.SetCodecFromSDP(sps, pps)
		r.motion.newEpoch(epoch, sps, pps)
		if r.manualWanted.Load() {
			controller.StartManual()
		}
		r.log("info", "Receiving Nest H264; 5-second pre-motion buffer enabled")
		go func() {
			// Pion SampleBuilder reorders RTP packets and reconstructs complete
			// frames before any MP4 writing. The old recorder wrote raw RTP
			// directly, causing corrupted first frames on packet loss.
			builder := samplebuilder.New(128, &codecs.H264Packet{}, 90000)
			var first uint32
			var firstAt time.Time
			started := false
			_ = peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}})
			lastPLI := time.Now()
			lastMotionPLI := lastPLI
			for {
				pkt, _, e := track.ReadRTP()
				if e != nil {
					signalEnd(e)
					return
				}
				builder.Push(pkt)
				for sample := builder.Pop(); sample != nil; sample = builder.Pop() {
					if len(sample.Data) == 0 {
						continue
					}
					if !started {
						first = sample.PacketTimestamp
						firstAt = time.Now()
						started = true
					}
					ticks := uint32(sample.PacketTimestamp - first)
					pts := time.Duration(uint64(ticks) * uint64(time.Second) / 90000)
					mapped := firstAt.Add(pts)
					_ = controller.Push(preroll.Unit{At: mapped, PTS: pts, Data: sample.Data, Epoch: epoch})
					r.motion.offer(epoch, int64(pts/time.Microsecond), sample.Data)
					if sample.PrevDroppedPackets > 0 {
						r.packetLoss.Add(uint64(sample.PrevDroppedPackets))
					}
				}
				if time.Since(lastMotionPLI) > 2*time.Second && r.motion.needsKeyframe(epoch) {
					_ = peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}})
					lastMotionPLI = time.Now()
				}
				if time.Since(lastPLI) > 8*time.Second {
					if controller.Status().CachedFrames == 0 {
						_ = peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}})
					}
					lastPLI = time.Now()
				}
			}
		}()
	})
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		return err
	}
	gather := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(offer); err != nil {
		return err
	}
	select {
	case <-gather:
	case <-time.After(18 * time.Second):
		return errors.New("ICE gathering timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
	sdp := peer.LocalDescription().SDP
	if !strings.HasSuffix(sdp, "\n") {
		sdp += "\r\n"
	}
	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
	data, err := apiRequest(reqCtx, "POST", "https://smartdevicemanagement.googleapis.com/v1/"+c.DeviceName+":executeCommand", token,
		map[string]any{"command": "sdm.devices.commands.CameraLiveStream.GenerateWebRtcStream", "params": map[string]string{"offerSdp": sdp}})
	reqCancel()
	if err != nil {
		return err
	}
	var response struct {
		Results struct {
			AnswerSdp      string `json:"answerSdp"`
			MediaSessionID string `json:"mediaSessionId"`
			ExpiresAt      string `json:"expiresAt"`
		} `json:"results"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return err
	}
	if response.Results.AnswerSdp == "" {
		return errors.New("Nest returned empty WebRTC answer")
	}
	if err = peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: response.Results.AnswerSdp}); err != nil {
		return err
	}
	mediaSession := response.Results.MediaSessionID
	if mediaSession == "" {
		return errors.New("Nest did not return mediaSessionId for renewal")
	}
	expiry := time.Now().Add(5 * time.Minute)
	if t, e := time.Parse(time.RFC3339Nano, response.Results.ExpiresAt); e == nil {
		expiry = t
	}
	r.log("info", "Persistent WebRTC active; will extend before session expiration")
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer stopCancel()
		if tok, e := accessToken(c); e == nil {
			_, _ = apiRequest(stopCtx, "POST", "https://smartdevicemanagement.googleapis.com/v1/"+c.DeviceName+":executeCommand", tok,
				map[string]any{"command": "sdm.devices.commands.CameraLiveStream.StopWebRtcStream", "params": map[string]string{"mediaSessionId": mediaSession}})
		}
	}()

	for {
		renewIn := time.Until(expiry.Add(-55 * time.Second))
		if renewIn < 5*time.Second {
			renewIn = 5 * time.Second
		}
		timer := time.NewTimer(renewIn)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case e := <-ended:
			timer.Stop()
			return e
		case <-r.configChanged:
			timer.Stop()
			return errors.New("settings changed; reconnecting")
		case <-timer.C:
		}
		// Catch changes in selected SDM camera or OAuth authorization.
		current := r.loadConfig()
		if current.DeviceName != c.DeviceName || current.RefreshToken != c.RefreshToken || current.ClientID != c.ClientID {
			return errors.New("Google account/device changed; reconnecting")
		}
		token, err = accessToken(c)
		if err != nil {
			return err
		}
		extendCtx, extendCancel := context.WithTimeout(ctx, 30*time.Second)
		extended, err := apiRequest(extendCtx, "POST", "https://smartdevicemanagement.googleapis.com/v1/"+c.DeviceName+":executeCommand", token,
			map[string]any{"command": "sdm.devices.commands.CameraLiveStream.ExtendWebRtcStream", "params": map[string]string{"mediaSessionId": mediaSession}})
		extendCancel()
		if err != nil {
			return fmt.Errorf("Nest WebRTC extension failed (battery cameras cannot extend unplugged): %w", err)
		}
		var next struct {
			Results struct {
				ExpiresAt      string `json:"expiresAt"`
				MediaSessionID string `json:"mediaSessionId"`
			} `json:"results"`
		}
		if err = json.Unmarshal(extended, &next); err != nil {
			return err
		}
		if next.Results.MediaSessionID != "" {
			mediaSession = next.Results.MediaSessionID
		}
		t, e := time.Parse(time.RFC3339Nano, next.Results.ExpiresAt)
		if e != nil {
			return errors.New("Nest returned no valid session expiration on extend")
		}
		expiry = t
		r.log("info", "WebRTC session extended")
	}
}
