// signup_run.go — DEV-ONLY automated free-account signup (quota research).
//
// Flow: mail.tm temp inbox → Aliyun captcha (auth scene 36qgs6xb, solved with
// the same device-token machinery as chat) → POST /api/v1/auths/signup →
// poll inbox for the verification code → POST /api/v1/auths/verify_email →
// account JWT. Prints the JWT so the caller can run quota experiments.
package app

import (
        "bytes"
        "encoding/json"
        "flag"
        "fmt"
        "io"
        "log"
        "math/rand"
        "net/http"
        "os"
        "regexp"
        "strings"
        "time"

        fhttp "github.com/bogdanfinn/fhttp"
)

// ── mail.tm client ──

type mailTM struct {
        base    string
        address string
        password string
        token   string
        http    *http.Client
}

func newMailTM() *mailTM {
        return &mailTM{base: "https://api.mail.tm", http: &http.Client{Timeout: 20 * time.Second}}
}

func (m *mailTM) api(method, path string, body interface{}, auth bool) ([]byte, int, error) {
        var rd io.Reader
        if body != nil {
                b, _ := json.Marshal(body)
                rd = bytes.NewReader(b)
        }
        req, err := http.NewRequest(method, m.base+path, rd)
        if err != nil {
                return nil, 0, err
        }
        req.Header.Set("Accept", "application/json")
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }
        if auth && m.token != "" {
                req.Header.Set("Authorization", "Bearer "+m.token)
        }
        resp, err := m.http.Do(req)
        if err != nil {
                return nil, 0, err
        }
        defer resp.Body.Close()
        data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
        return data, resp.StatusCode, nil
}

// create makes a fresh inbox; retries with a new local part on 422/409.
func (m *mailTM) create(domain, password string) error {
        for i := 0; i < 6; i++ {
                local := fmt.Sprintf("zq.probe.%d%d", time.Now().UnixMilli()%1e6, rand.Intn(900)+100)
                addr := local + "@" + domain
                data, code, err := m.api("POST", "/accounts", map[string]string{"address": addr, "password": password}, false)
                if err != nil {
                        return err
                }
                if code == 201 {
                        var created struct {
                                Address string `json:"address"`
                        }
                        _ = json.Unmarshal(data, &created)
                        if created.Address != "" {
                                m.address = created.Address // mail.tm normalizes (strips dots) — use the canonical form
                        } else {
                                m.address = addr
                        }
                        m.password = password
                        // account propagation on mail.tm can lag a beat
                        var lastErr error
                        for j := 0; j < 5; j++ {
                                time.Sleep(time.Duration(1+j) * time.Second)
                                if lastErr = m.login(); lastErr == nil {
                                        return nil
                                }
                        }
                        return lastErr
                }
                log.Printf("[signup] inbox create HTTP %d: %s — retrying", code, truncStr(string(data), 200))
                time.Sleep(700 * time.Millisecond)
        }
        return fmt.Errorf("could not create inbox after retries")
}

func (m *mailTM) login() error {
        data, code, err := m.api("POST", "/token", map[string]string{"address": m.address, "password": m.password}, false)
        if err != nil || code != 200 {
                return fmt.Errorf("login: %d %s", code, string(data))
        }
        var out struct {
                Token string `json:"token"`
        }
        if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
                return fmt.Errorf("login parse: %s", string(data))
        }
        m.token = out.Token
        return nil
}

var codeRe = regexp.MustCompile(`\b(\d{4,8})\b`)

// waitForCode polls the inbox for a z.ai verification mail and returns the code.
func (m *mailTM) waitForCode(timeout time.Duration) (string, error) {
        deadline := time.Now().Add(timeout)
        for time.Now().Before(deadline) {
                data, code, err := m.api("GET", "/messages?page=1", nil, true)
                if err == nil && code == 200 {
                        var list struct {
                                Members []struct {
                                        ID      string `json:"id"`
                                        Subject string `json:"subject"`
                                        From    struct {
                                                Address string `json:"address"`
                                        } `json:"from"`
                                } `json:"hydra:member"`
                        }
                        if json.Unmarshal(data, &list) == nil {
                                for _, msg := range list.Members {
                                        if !strings.Contains(strings.ToLower(msg.From.Address), "z.ai") &&
                                                !strings.Contains(strings.ToLower(msg.Subject), "z.ai") &&
                                                !strings.Contains(strings.ToLower(msg.Subject), "verif") {
                                                continue
                                        }
                                        body, _, err := m.api("GET", "/messages/"+msg.ID, nil, true)
                                        if err != nil {
                                                continue
                                        }
                                        var full struct {
                                                Text string `json:"text"`
                                                HTML []string `json:"html"`
                                        }
                                        _ = json.Unmarshal(body, &full)
                                        blob := full.Text
                                        if blob == "" && len(full.HTML) > 0 {
                                                blob = full.HTML[0]
                                        }
                                        if ms := codeRe.FindStringSubmatch(blob); len(ms) == 2 {
                                                return ms[1], nil
                                        }
                                }
                        }
                }
                time.Sleep(5 * time.Second)
        }
        return "", fmt.Errorf("no verification mail within %v", timeout)
}

// ── signup flow against chat.z.ai ──

func zaiPostJSON(client httpDoer, url string, payload map[string]interface{}, token string) ([]byte, int, error) {
        b, _ := json.Marshal(payload)
        req, _ := fhttp.NewRequest("POST", url, bytes.NewReader(b))
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Accept", "application/json")
        req.Header.Set("Origin", baseURL)
        req.Header.Set("Referer", baseURL+"/")
        req.Header.Set("User-Agent", zaiUserAgent)
        if token != "" {
                req.Header.Set("Authorization", "Bearer "+token)
        }
        resp, err := client.Do(req)
        if err != nil {
                return nil, 0, err
        }
        defer resp.Body.Close()
        data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
        return data, resp.StatusCode, nil
}

// RunSignup executes the automated signup and returns/prints the account JWT.
func RunSignup() {
        fs := flag.NewFlagSet("zsignup", flag.ExitOnError)
        dbPath := fs.String("db", "tokens.sqlite", "device-token sqlite for captcha")
        name := fs.String("name", "Probe User", "account display name")
        password := fs.String("password", "", "account password (generated if empty)")
        mailDomain := fs.String("maildomain", "fldubai.com", "mail.tm domain")
        emailOverride := fs.String("email", "", "use this email instead of a temp inbox")
        scene := fs.String("scene", "36qgs6xb", "captcha scene override")
        outFile := fs.String("out", "", "write JWT to this file")
        fs.Parse(os.Args[1:])

        if *password == "" {
                *password = fmt.Sprintf("Zq%d!%c%c", rand.Intn(900000)+100000, 'A'+rune(rand.Intn(26)), 'a'+rune(rand.Intn(26)))
        }

        // 1. temp inbox (or -email override)
        mail := newMailTM()
        useMail := false
        if *emailOverride != "" {
                mail.address = *emailOverride
        } else if err := mail.create(*mailDomain, *password); err != nil {
                log.Fatalf("[signup] inbox: %v", err)
        } else {
                useMail = true
        }
        log.Printf("[signup] email: %s (temp inbox: %v)", mail.address, useMail)

        // 2. captcha with the AUTH scene
        captchaSceneOverride = *scene
        captchaMaxRetries = 8
        cfg := &Config{Host: "127.0.0.1", Port: "5998", AuthEnabled: true, AuthToken: "probe",
                LogLevel: "info", Timeout: 5 * time.Minute, DBPath: *dbPath, CaptchaTimeout: 60 * time.Second}
        tokenStore, err := OpenTokenStore(*dbPath)
        if err != nil {
                log.Fatalf("[signup] token store: %v", err)
        }
        defer tokenStore.Close()
        captchaVerbose = true
        srv := &Server{cfg: cfg, zaiSession: newZaiSession(), conversations: newConversationStore(), tokens: tokenStore}
        captchaParam := computeCaptchaParam(srv, tokenStore)
        if captchaParam == "" {
                log.Fatalf("[signup] captcha failed")
        }
        log.Printf("[signup] captcha ok (%d chars)", len(captchaParam))

        client, err := zaiHTTP(nil)
        if err != nil {
                log.Fatalf("[signup] http client: %v", err)
        }

        // 3. signup
        signupBody := map[string]interface{}{
                "name":                *name,
                "email":               mail.address,
                "password":            *password,
                "profile_image_url":   "",
                "sso_redirect":        "/",
                "captcha_verify_param": captchaParam,
        }
        data, code, err := zaiPostJSON(client, baseURL+"/api/v1/auths/signup", signupBody, "")
        if err != nil {
                log.Fatalf("[signup] request: %v", err)
        }
        log.Printf("[signup] signup HTTP %d: %s", code, truncStr(string(data), 400))
        if code != 200 {
                log.Fatalf("[signup] signup rejected")
        }

        // 4. wait for the code email
        if !useMail {
                log.Fatalf("[signup] email override set — stopping after signup response")
        }
        verCode, err := mail.waitForCode(3 * time.Minute)
        if err != nil {
                log.Fatalf("[signup] mail: %v", err)
        }
        log.Printf("[signup] verification code: %s", verCode)

        // 5. verify_email → JWT
        verifyBody := map[string]interface{}{
                "username": *name,
                "email":    mail.address,
                "token":    verCode,
        }
        data, code, err = zaiPostJSON(client, baseURL+"/api/v1/auths/verify_email", verifyBody, "")
        if err != nil {
                log.Fatalf("[signup] verify request: %v", err)
        }
        log.Printf("[signup] verify HTTP %d: %s", code, truncStr(string(data), 600))
        if code != 200 {
                log.Fatalf("[signup] verify failed")
        }
        var out struct {
                Token string `json:"token"`
        }
        if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
                log.Fatalf("[signup] no JWT in verify response")
        }
        fmt.Printf("\nEMAIL=%s\nPASSWORD=%s\nJWT=%s\n", mail.address, *password, out.Token)
        if *outFile != "" {
                if err := writeJSONOut(*outFile, mail.address, *password, out.Token); err != nil {
                        log.Printf("[signup] write out: %v", err)
                }
        }
}

func writeJSONOut(path, email, password, jwt string) error {
        b, _ := json.MarshalIndent(map[string]string{"email": email, "password": password, "token": jwt}, "", "  ")
        return os.WriteFile(path, b, 0o600)
}

func truncStr(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[:n] + "..."
}
