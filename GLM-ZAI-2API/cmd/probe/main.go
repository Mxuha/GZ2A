// probe — dev entry point: raw SSE probe for Z.AI chat completions.
// Usage: probe [-prompt ...] [-model ...] [-img] [-think=false] [-effort ...]
package main

import app "glm-zai-2api/internal/app"

func main() { app.RunProbe() }
