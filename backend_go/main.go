package main

import (
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const (
	googleAuthEndpoint         = "https://accounts.google.com/o/oauth2/v2/auth"
	googleUserinfoEndpoint     = "https://openidconnect.googleapis.com/v1/userinfo"
	frontendSetupURL           = "http://localhost:3000/setup/llm"
	geminiEnableAPIURL         = "https://console.cloud.google.com/flows/enableapi?apiid=generativelanguage.googleapis.com"
	googleGeminiRetrieverScope = "https://www.googleapis.com/auth/generative-language.retriever"
	maxToolCallIterations      = 20
	streamSessionTTL           = 15 * time.Minute
)

var appInstance *App
