// probe_run.go — DEV-ONLY raw SSE probe for Z.AI chat completions.
// Reuses the package internals (session, captcha, signature, tls-client)
// to send a request with configurable features and dump every raw SSE
// event to stdout. Not wired into the server; used via cmd/probe.
package app

import (
        "bufio"
        "bytes"
        "context"
        "encoding/json"
        "flag"
        "fmt"
        "io"
        "log"
        "net/url"
        "os"
        "strings"
        "time"

        fhttp "github.com/bogdanfinn/fhttp"
)

// ProbeOptions configures a single raw probe request.
type ProbeOptions struct {
        Prompt        string
        Model         string
        ImageGen      bool
        Thinking      bool
        ReasoningEffort string
        MCPServers    []string
        AutoWebSearch bool
        AgentMode     bool
        ExtraFeatures map[string]interface{}
}

// RunProbe executes one raw SSE request and prints every event line.
func RunProbe() {
        opts := ProbeOptions{
                Prompt:          "Say hi in one word.",
                Model:           "glm-5.3",
                ImageGen:        false,
                Thinking:        true,
                ReasoningEffort: "",
        }
        fs := flag.NewFlagSet("probe", flag.ExitOnError)
        fs.StringVar(&opts.Prompt, "prompt", opts.Prompt, "prompt text")
        fs.StringVar(&opts.Model, "model", opts.Model, "model id")
        fs.BoolVar(&opts.ImageGen, "img", false, "features.image_generation = true")
        fs.BoolVar(&opts.Thinking, "think", true, "features.enable_thinking")
        fs.StringVar(&opts.ReasoningEffort, "effort", "", "reasoning_effort")
        mcp := fs.String("mcp", "", "comma-separated mcp_servers")
        fs.BoolVar(&opts.AutoWebSearch, "search", false, "features.auto_web_search = true")
        fs.BoolVar(&opts.AgentMode, "agent", false, "chat type general_agent (merged agent mode)")
        captchaMode := fs.String("captcha", "try", "try|skip|empty")
        tokenEnv := fs.String("token", "", "ZAI token (defaults to ZAI_TOKEN env)")
        features := fs.String("features", "", "extra features as k=v,k=v (bool or string)")
        fs.Parse(os.Args[1:])

        if *mcp != "" {
                opts.MCPServers = strings.Split(*mcp, ",")
        }
        if *features != "" {
                opts.ExtraFeatures = map[string]interface{}{}
                for _, kv := range strings.Split(*features, ",") {
                        parts := strings.SplitN(kv, "=", 2)
                        if len(parts) == 2 {
                                if parts[1] == "true" || parts[1] == "false" {
                                        opts.ExtraFeatures[parts[0]] = parts[1] == "true"
                                } else {
                                        opts.ExtraFeatures[parts[0]] = parts[1]
                                }
                        }
                }
        }

        token := *tokenEnv
        if token == "" {
                token = os.Getenv("ZAI_TOKEN")
        }
        if token == "" {
                log.Fatalf("no token: set ZAI_TOKEN or -token")
        }

        cfg := &Config{
                Host: "127.0.0.1", Port: "5999", AuthEnabled: true, AuthToken: "probe",
                ZAIToken: token, LogLevel: "debug",
                Timeout: 5 * time.Minute, DBPath: envOr("PROBE_DB", "tokens.sqlite"), CaptchaTimeout: 60 * time.Second,
        }
        zs := newZaiSession()
        if v := os.Getenv("PROBE_FE"); v != "" { zs.feVersion = v }
        captchaVerbose = true
        var tokenStore *TokenStore
        if ts, err := OpenTokenStore(cfg.DBPath); err == nil {
                tokenStore = ts
                defer ts.Close()
        } else {
                log.Printf("[probe] token store unavailable: %v", err)
        }
        srv := &Server{cfg: cfg, zaiSession: zs, conversations: newConversationStore(), tokens: tokenStore}

        if err := zs.initialize(token); err != nil {
                log.Fatalf("session init: %v", err)
        }
        log.Printf("[probe] session ok: user=%s fe=%s", zs.userName, zs.feVersion)

        chatID, msgID, err := srv.createZaiChat(opts.Model, []json.RawMessage{
                json.RawMessage(`{"role":"user","content":` + quoteJSON(opts.Prompt) + `}`),
        }, opts.MCPServers, opts.AutoWebSearch, opts.Thinking, opts.ReasoningEffort, opts.AgentMode)
        if err != nil {
                log.Fatalf("create chat: %v", err)
        }
        log.Printf("[probe] chat=%s msg=%s", chatID, msgID)

        signature, urlParams := zs.generateSignatureWithParams(opts.Prompt, token, zs.userID, chatID)

        var flagsVal interface{} = []interface{}{}
        if opts.AgentMode {
                flagsVal = []interface{}{"general_agent"}
        }
        featuresMap := map[string]interface{}{
                "image_generation": opts.ImageGen,
                "web_search":       false,
                "auto_web_search":  opts.AutoWebSearch,
                "preview_mode":     false,
                "flags":            flagsVal,
                "enable_thinking":  opts.Thinking,
                "vlm_tools_enable": false,
                "vlm_web_search_enable": false,
                "vlm_website_mode": false,
        }
        if opts.ReasoningEffort != "" {
                featuresMap["reasoning_effort"] = opts.ReasoningEffort
        } else if opts.AgentMode {
                featuresMap["reasoning_effort"] = "max" // webui parity: agent mode forces max effort
        }
        for k, v := range opts.ExtraFeatures {
                featuresMap[k] = v
        }

        captchaParam := ""
        switch *captchaMode {
        case "skip":
                // omit the field entirely
        case "empty":
                captchaParam = ""
        default:
                captchaParam = computeCaptchaParam(srv, tokenStore)
                if captchaParam == "" {
                        log.Fatalf("captcha failed")
                }
                log.Printf("[probe] captcha ok (%d chars)", len(captchaParam))
        }

        body := map[string]interface{}{
                "model":               opts.Model,
                "chat_id":             chatID,
                "messages":            []map[string]interface{}{{"role": "user", "content": opts.Prompt}},
                "signature_prompt":    opts.Prompt,
                "stream":              true,
                "features":            featuresMap,
                "params":              map[string]interface{}{},
                "extra":               map[string]interface{}{},
                "variables": map[string]string{
                        "{{USER_NAME}}":        zs.userName,
                        "{{USER_LOCATION}}":    "Unknown",
                        "{{CURRENT_DATETIME}}": time.Now().Format("2006-01-02 15:04:05"),
                        "{{CURRENT_DATE}}":     time.Now().Format("2006-01-02"),
                        "{{CURRENT_TIME}}":     time.Now().Format("15:04:05"),
                        "{{CURRENT_WEEKDAY}}":  time.Now().Weekday().String(),
                        "{{CURRENT_TIMEZONE}}": "Africa/Algiers",
                        "{{USER_LANGUAGE}}":    "en-US",
                },
                "background_tasks":               map[string]bool{"title_generation": true, "tags_generation": true},
                "id":                             uuidV4(),
                "current_user_message_id":        msgID,
                "current_user_message_parent_id": nil,
        }
        if captchaParam != "" {
                body["captcha_verify_param"] = captchaParam
        }
        if len(opts.MCPServers) > 0 {
                body["mcp_servers"] = opts.MCPServers
        }
        bodyBytes, _ := json.Marshal(body)
        fmt.Fprintf(os.Stderr, "=== request features ===\n%s\n", mustJSONIndent(featuresMap))

        headers := map[string]string{
                "authorization": "Bearer " + token,
                "User-Agent":    zaiUserAgent,
                "content-type":  "application/json",
                "Origin":        baseURL,
                "Referer":       baseURL + "/",
                "x-fe-Version":  zs.feVersion,
                "x-region":      "overseas",
                "X-Signature":   signature,
        }
        zaiJar.SetCookies(&url.URL{Scheme: "https", Host: "chat.z.ai"},
                []*fhttp.Cookie{{Name: "token", Value: token}})

        client, err := zaiHTTP(nil)
        if err != nil {
                log.Fatalf("http client: %v", err)
        }
        ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
        defer cancel()
        req, _ := fhttp.NewRequestWithContext(ctx, "POST", baseURL+"/api/v2/chat/completions?"+urlParams, bytes.NewReader(bodyBytes))
        for k, v := range headers {
                req.Header.Set(k, v)
        }
        resp, err := client.Do(req)
        if err != nil {
                log.Fatalf("request: %v", err)
        }
        defer resp.Body.Close()
        fmt.Fprintf(os.Stderr, "=== response status: %d ===\n", resp.StatusCode)
        if resp.StatusCode != 200 {
                b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
                fmt.Fprintf(os.Stderr, "=== error body: %s\n", string(b))
                return
        }

        reader := bufio.NewReaderSize(resp.Body, 256*1024)
        n := 0
        for {
                line, err := reader.ReadString('\n')
                if l := strings.TrimSpace(line); l != "" {
                        n++
                        fmt.Println("RAW|" + l)
                }
                if err != nil {
                        break
                }
        }
        fmt.Fprintf(os.Stderr, "=== stream ended: %d event lines ===\n", n)
}

func quoteJSON(s string) string {
        b, _ := json.Marshal(s)
        return string(b)
}

func mustJSONIndent(v interface{}) string {
        b, _ := json.MarshalIndent(v, "", " ")
        return string(b)
}
