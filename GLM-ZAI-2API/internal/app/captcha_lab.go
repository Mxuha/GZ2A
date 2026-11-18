// captcha_lab.go — DEV-ONLY experiment harness for the auth captcha scene.
// Tries {attach, fresh-fingerprint, mode} combos against scene 36qgs6xb and
// reports Aliyun VerifyResult for each, so we know what the risk engine wants.
package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"time"
)

// RunCaptchaLab tries several strategies for solving the auth-scene captcha.
func RunCaptchaLab() {
	ts, err := OpenTokenStore("lab2.sqlite")
	if err != nil {
		log.Fatalf("[lab] token store: %v", err)
	}
	defer ts.Close()

	strategies := []struct {
		name   string
		scene  string
		mode   string
		fresh  bool // synthesize a fresh SG_WEB fingerprint instead of store token
		attach bool
	}{
		{"auth-embed-real-attach", "36qgs6xb", "embed", false, true},
		{"auth-embed-real-noattach", "36qgs6xb", "embed", false, false},
		{"auth-popup-real-noattach", "36qgs6xb", "popup", false, false},
		{"chat-popup-real-attach", "didk33e0", "popup", false, true},
		{"chat-popup-real-noattach", "didk33e0", "popup", false, false},
	}

	for _, st := range strategies {
		ok, tries := 0, 5
		for i := 0; i < tries; i++ {
			if labAttempt(ts, st.scene, st.mode, st.fresh, st.attach) {
				ok++
			}
			time.Sleep(400 * time.Millisecond)
		}
		log.Printf("[lab] %-28s -> %d/%d verify OK", st.name, ok, tries)
	}
}

func labAttempt(ts *TokenStore, scene, mode string, fresh, attach bool) bool {
	captchaSceneOverride = scene
	captchaModeOverride = mode
	defer func() { captchaModeOverride = "" }()

	deviceToken := ""
	var dd string
	ddOK := false

	if fresh {
		fpBytes := make([]byte, 16)
		rand.Read(fpBytes)
		fp := hex.EncodeToString(fpBytes)
		raw := fmt.Sprintf("SG_WEB#%s-h-%d-%x", fp, time.Now().UnixMilli(), fpBytes[:4])
		deviceToken = base64.StdEncoding.EncodeToString([]byte(raw))
		if attach {
			if dd2, err := generateDeviceData(scene, "no8xfe", "sgp", fp); err == nil {
				dd, ddOK = dd2, true
			}
		}
	} else {
		real, ok := ts.getNext()
		if !ok {
			return false
		}
		deviceToken = real
		if attach {
			fp := fingerprintFromToken(real)
			if dd2, err := generateDeviceData(scene, "no8xfe", "sgp", fp); err == nil {
				dd, ddOK = dd2, true
			}
		}
	}

	payload, err := captchaPayloadOnceWithDeviceData(deviceToken, dd, ddOK && attach)
	if err != nil {
		return false
	}
	return payload != ""
}
