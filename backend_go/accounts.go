package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	accountCookieName = "account_id"
	sessionCookieName = "session_id"
)

var accountEmailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type AccountServer struct {
	config     Config
	accountDir string

	mu       sync.RWMutex
	accounts map[string]*App
}

func newAccountServer(config Config) (*AccountServer, error) {
	accountDir := filepath.Dir(config.DatabasePath)
	if err := os.MkdirAll(accountDir, 0o755); err != nil {
		return nil, err
	}
	server := &AccountServer{
		config:     config,
		accountDir: accountDir,
		accounts:   make(map[string]*App),
	}
	if err := server.loadAccountDatabases(); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *AccountServer) loadAccountDatabases() error {
	paths, err := filepath.Glob(filepath.Join(s.accountDir, "*.db"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		accountID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if !isCanonicalAccountID(accountID) {
			continue
		}
		if _, err := s.openAccount(accountID, path); err != nil {
			return err
		}
	}
	return nil
}

func (s *AccountServer) openAccount(accountID string, databasePath string) (*App, error) {
	cfg := s.config
	cfg.AccountID = accountID
	cfg.DatabasePath = databasePath
	app, err := newAccountApp(cfg, true)
	if err != nil {
		return nil, err
	}
	s.accounts[accountID] = app
	return app, nil
}

func (s *AccountServer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, app := range s.accounts {
		_ = app.close()
	}
}

func (s *AccountServer) getOrCreateAccountForEmail(email string) (*App, string, error) {
	accountID, err := accountIDForEmail(email)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if app := s.accounts[accountID]; app != nil {
		return app, accountID, nil
	}
	databasePath := filepath.Join(s.accountDir, accountID+".db")
	app, err := s.openAccount(accountID, databasePath)
	if err != nil {
		return nil, "", err
	}
	return app, accountID, nil
}

func (s *AccountServer) selectedAccount(r *http.Request) (*App, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cookie, err := r.Cookie(accountCookieName); err == nil {
		accountID := strings.TrimSpace(cookie.Value)
		if app := s.accounts[accountID]; app != nil {
			return app, accountID
		}
	}
	ids := s.accountIDsLocked()
	if len(ids) == 0 {
		return nil, ""
	}
	accountID := ids[0]
	return s.accounts[accountID], accountID
}

func (s *AccountServer) accountByID(accountID string) (*App, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app := s.accounts[accountID]
	return app, app != nil
}

func (s *AccountServer) accountIDsLocked() []string {
	ids := make([]string, 0, len(s.accounts))
	for accountID := range s.accounts {
		ids = append(ids, accountID)
	}
	sort.Strings(ids)
	return ids
}

func (s *AccountServer) accountSummaries(activeAccountID string) []AccountSummary {
	s.mu.RLock()
	ids := s.accountIDsLocked()
	apps := make([]*App, 0, len(ids))
	for _, accountID := range ids {
		apps = append(apps, s.accounts[accountID])
	}
	s.mu.RUnlock()

	summaries := make([]AccountSummary, 0, len(apps))
	for index, app := range apps {
		if app == nil {
			continue
		}
		accountID := ids[index]
		summary := AccountSummary{
			AccountID: accountID,
			Active:    accountID == activeAccountID,
		}
		state, err := app.getSetupState()
		if err == nil {
			summary.Email = state.GoogleEmail
			summary.Name = state.GoogleName
			summary.Picture = state.GooglePicture
			summary.GoogleAccountConnected = state.GoogleAccountConnected
			summary.OnboardingCompleted = state.OnboardingCompleted
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (s *AccountServer) emptySetupStatus(activeAccountID string) SetupStatus {
	return SetupStatus{
		ActiveAccountID: activeAccountID,
		Accounts:        s.accountSummaries(activeAccountID),
		AvailableAgents: agentDefinitions,
		GmailTools:      allToolDefinitionsSnapshot(),
	}
}

func accountIDForEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if !accountEmailPattern.MatchString(normalized) {
		return "", errors.New("account_email must be a valid email address")
	}
	var builder strings.Builder
	lastWasSeparator := false
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastWasSeparator = false
		case r == '@' || r == '.':
			if !lastWasSeparator && builder.Len() > 0 {
				builder.WriteString("__")
				lastWasSeparator = true
			}
		case r == '_' || r == '-':
			builder.WriteRune(r)
			lastWasSeparator = false
		default:
			if !lastWasSeparator && builder.Len() > 0 {
				builder.WriteByte('_')
				lastWasSeparator = true
			}
		}
	}
	accountID := strings.Trim(builder.String(), "_")
	if !isCanonicalAccountID(accountID) {
		return "", errors.New("account_email could not be converted to a valid database name")
	}
	return accountID, nil
}

func isCanonicalAccountID(accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || strings.Contains(accountID, ".") || strings.Contains(accountID, string(filepath.Separator)) {
		return false
	}
	if !strings.Contains(accountID, "__") {
		return false
	}
	for _, r := range accountID {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func setAccountCookie(w http.ResponseWriter, accountID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     accountCookieName,
		Value:    accountID,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60,
	})
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
}
