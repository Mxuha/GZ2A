// zsignup — dev entry point: automated chat.z.ai free-account signup.
// Usage: zsignup -db tokens.sqlite -out account.json
package main

import app "glm-zai-2api/internal/app"

func main() { app.RunSignup() }
