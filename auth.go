package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/chat/v1"
)

const keyringService = "mutter"

var scopes = []string{
	"openid",
	"email",
	chat.ChatSpacesReadonlyScope,
	chat.ChatMessagesScope,
	chat.ChatMembershipsReadonlyScope,
}

type config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (c config) oauth() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       scopes,
	}
}

// tokenSource returns a token source that persists refreshed tokens to the
// OS keychain. It runs the browser login flow when no usable token is stored.
func tokenSource(ctx context.Context, cfg *oauth2.Config) (oauth2.TokenSource, error) {
	tok, err := loadToken()
	if err != nil {
		if tok, err = login(ctx, cfg); err != nil {
			return nil, err
		}
		if err := saveToken(tok); err != nil {
			return nil, err
		}
	}
	return &savingSource{src: cfg.TokenSource(ctx, tok), last: tok.AccessToken}, nil
}

type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if err := saveToken(tok); err != nil {
			return nil, err
		}
	}
	return tok, nil
}

func loadToken() (*oauth2.Token, error) {
	s, err := keyring.Get(keyringService, "token")
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(s), &tok); err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("stored token has no refresh token")
	}
	return &tok, nil
}

func saveToken(tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, "token", string(b))
}

func logout() error {
	return keyring.Delete(keyringService, "token")
}

// login runs the installed-app loopback flow with PKCE.
func login(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	c := *cfg
	c.RedirectURL = fmt.Sprintf("http://%s/", ln.Addr())
	state := randHex()
	verifier := oauth2.GenerateVerifier()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			ch <- result{err: errors.New("oauth state mismatch")}
		case q.Get("error") != "":
			http.Error(w, q.Get("error"), http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("oauth: %s", q.Get("error"))}
		default:
			fmt.Fprintln(w, "mutter is logged in. You can close this tab.")
			ch <- result{code: q.Get("code")}
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	url := c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	fmt.Printf("Opening browser for login. If it does not open, visit:\n%s\n", url)
	openBrowser(url)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return c.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	}
}

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	_ = exec.Command(cmd, url).Start()
}

func randHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
